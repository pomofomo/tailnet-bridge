// Package caddyproc manages the Caddy child process (SPEC §9.4).
//
// Caddy is spawned with a minimal bootstrap config (admin API only); all
// further configuration arrives via the admin /load endpoint. Caddy's
// stdout and stderr are piped to this process's stderr with a "caddy: "
// prefix.
//
// If Caddy exits unexpectedly, Wait returns and the orchestrator exits
// non-zero. We do NOT attempt in-place restart — that risks state
// divergence (SPEC §9.4).
package caddyproc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Proc is a running Caddy child.
type Proc struct {
	cmd     *exec.Cmd
	pumpsWG sync.WaitGroup
}

// Start launches caddy with the given bootstrap config path:
//
//	caddy run --config <bootstrap> --resume=false
//
// stdout/stderr are line-prefixed with "caddy: " and forwarded to this
// process's stderr.
func Start(ctx context.Context, caddyBin, bootstrapPath string) (*Proc, error) {
	if caddyBin == "" {
		caddyBin = "caddy"
	}
	cmd := exec.CommandContext(ctx, caddyBin, "run", "--config", bootstrapPath, "--resume=false")
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("caddyproc: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("caddyproc: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("caddyproc: start %s: %w", caddyBin, err)
	}

	p := &Proc{cmd: cmd}
	p.pumpsWG.Add(2)
	go p.pump(stdout)
	go p.pump(stderr)
	return p, nil
}

func (p *Proc) pump(r io.Reader) {
	defer p.pumpsWG.Done()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			fmt.Fprint(os.Stderr, "caddy: ", line)
			if line[len(line)-1] != '\n' {
				fmt.Fprintln(os.Stderr)
			}
		}
		if err != nil {
			return
		}
	}
}

// Wait blocks until the child exits and the output pumps drain.
func (p *Proc) Wait() error {
	err := p.cmd.Wait()
	p.pumpsWG.Wait()
	return err
}

// Signal forwards a signal to the child.
func (p *Proc) Signal(sig os.Signal) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return fmt.Errorf("caddyproc: not started")
	}
	return p.cmd.Process.Signal(sig)
}

// Terminate sends SIGTERM. Convenience for the lifecycle code in main.
func (p *Proc) Terminate() error {
	return p.Signal(syscall.SIGTERM)
}
