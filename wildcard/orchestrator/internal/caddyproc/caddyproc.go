// Package caddyproc manages the Caddy child process (SPEC §10.3).
//
// Caddy is spawned with a minimal bootstrap config (admin API only); all
// further configuration arrives via the admin /load endpoint. Caddy's
// stdout and stderr are piped to this process's stderr with a "caddy: "
// prefix.
//
// If Caddy exits unexpectedly, Wait returns and the orchestrator exits
// non-zero. We do NOT attempt in-place restart — that risks state
// divergence.
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
	cancel  context.CancelFunc
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	pumpsWG sync.WaitGroup
}

// Start launches caddy with the given bootstrap config path:
//
//	caddy run --config <bootstrap> --resume=false
//
// stdout/stderr are line-prefixed with "caddy: " and forwarded to this
// process's stderr. The pumps shut down when the child's pipes EOF OR
// when ctx is cancelled — whichever comes first — so a wedged pipe
// doesn't keep the goroutines alive forever.
func Start(ctx context.Context, caddyBin, bootstrapPath string) (*Proc, error) {
	if caddyBin == "" {
		caddyBin = "caddy"
	}
	pumpCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, caddyBin, "run", "--config", bootstrapPath, "--resume=false")
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("caddyproc: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("caddyproc: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("caddyproc: start %s: %w", caddyBin, err)
	}

	p := &Proc{cmd: cmd, cancel: cancel, stdout: stdout, stderr: stderr}
	p.pumpsWG.Add(2)
	go p.pump(pumpCtx, stdout)
	go p.pump(pumpCtx, stderr)
	return p, nil
}

// pump forwards r line-by-line to stderr with a "caddy: " prefix.
// Exits on reader EOF or ctx cancellation.
func (p *Proc) pump(ctx context.Context, r io.ReadCloser) {
	defer p.pumpsWG.Done()
	// Close the pipe when ctx is cancelled so the blocking ReadString
	// returns; without this, a child that never closes its pipes would
	// keep this goroutine alive forever.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-done:
		}
	}()

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
	// Ensure pumps stop even if the child somehow left a pipe open.
	if p.cancel != nil {
		p.cancel()
	}
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
