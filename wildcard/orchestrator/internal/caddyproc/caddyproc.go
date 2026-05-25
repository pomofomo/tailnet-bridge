// Package caddyproc manages the Caddy child process. The orchestrator
// is PID 1; Caddy is its only child; this package owns the spawn, log
// piping, signal forwarding, and graceful shutdown.
//
// Status: STUB.
package caddyproc

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// Caddy represents a running Caddy subprocess.
type Caddy struct {
	Binary        string // default: "caddy"
	BootstrapPath string // path to caddy/bootstrap.json
	Stdout        io.Writer
	Stderr        io.Writer

	cmd *exec.Cmd
}

// Start spawns Caddy with `run --config <bootstrap> --adapter caddyfile=false`
// equivalent. It returns once Caddy is launched but does NOT wait for the
// admin API to be ready — call WaitForAdmin separately.
//
// Both stdout and stderr are line-prefixed with "caddy: " and piped to
// the orchestrator's stderr.
func (c *Caddy) Start(ctx context.Context) error {
	// TODO(impl):
	//   1. Validate Binary and BootstrapPath.
	//   2. exec.CommandContext(ctx, c.Binary, "run", "--config",
	//      c.BootstrapPath, "--watch=false", ...).
	//   3. Wire stdout/stderr through a "caddy: "-prefixing scanner.
	//   4. cmd.Start.
	_ = ctx
	return errNotImplemented
}

// WaitForAdmin polls the Caddy admin TCP socket until it accepts a
// connection or the context is cancelled. Used at startup before the
// orchestrator POSTs the initial config.
func (c *Caddy) WaitForAdmin(ctx context.Context, addr string) error {
	// TODO(impl): dial loop with backoff; ctx-aware.
	_, _ = ctx, addr
	return errNotImplemented
}

// Shutdown sends SIGTERM to the Caddy process and waits up to `grace` for
// it to exit. Returns the process exit error (if any) or context.DeadlineExceeded
// if it didn't exit in time.
func (c *Caddy) Shutdown(ctx context.Context) error {
	// TODO(impl): SIGTERM, wait, force-kill on timeout.
	_ = ctx
	return errNotImplemented
}

// Wait blocks until Caddy exits and returns its exit error (if any).
// A nil return means clean exit (status 0).
func (c *Caddy) Wait() error {
	// TODO(impl): c.cmd.Wait, normalized.
	return errNotImplemented
}

var errNotImplemented = errors.New("caddyproc: not yet implemented")
