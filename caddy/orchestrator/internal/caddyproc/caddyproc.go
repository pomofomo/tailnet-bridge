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

import "context"

// Proc is a running Caddy child.
type Proc struct{}

// Start launches caddy with the given bootstrap config path.
//
//   caddy run --config <bootstrap> --resume=false
//
// TODO: pipe stdout/stderr with "caddy: " prefix; capture exit status.
func Start(ctx context.Context, caddyBin, bootstrapPath string) (*Proc, error) {
	return nil, nil
}

// Wait blocks until the child exits.
func (p *Proc) Wait() error { return nil }

// Signal forwards a signal to the child (used for SIGTERM/SIGINT on
// shutdown; we don't forward SIGHUP — SIGHUP is poller-internal).
func (p *Proc) Signal(sig interface{}) error { return nil }
