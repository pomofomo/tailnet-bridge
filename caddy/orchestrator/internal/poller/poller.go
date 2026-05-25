// Package poller drives the per-community polling loop (SPEC §9.1, §9.5).
//
// One goroutine per community owns that community's tsnet poller node and
// fetches its directory. A single mutex serializes
// (read directories) → (caddyconfig.Build) → (admin /load) so config
// regeneration is atomic. SIGHUP triggers an out-of-band fan-out poll.
package poller

import (
	"bridge/internal/adminclient"
	"bridge/internal/caddyconfig"
	"bridge/internal/config"
	"bridge/internal/health"
	"context"
)

// Deps is what the orchestrator wires in.
type Deps struct {
	Config *config.Config
	Health *health.Store
	Admin  *adminclient.Client
	// Build defaults to caddyconfig.Build; tests inject a fake.
	Build func(caddyconfig.Input) ([]byte, error)
}

// Run blocks until ctx is cancelled. It spawns one poller goroutine per
// community plus a SIGHUP listener (signal handling lives in main; main
// calls TriggerAll to fan out).
//
// TODO:
//   - bring up one tsnet.Server per community (Ephemeral: true)
//   - on each tick / SIGHUP / startup: directory.Fetch through that tsnet
//   - on success: store in Health, mark fresh-since
//   - serialize regeneration: hash the merged JSON; only POST on change
//   - on Caddy validate error: log + retain prior config; do NOT rapid-retry
func Run(ctx context.Context, deps Deps) error {
	return nil
}

// TriggerAll is invoked by the signal handler on SIGHUP.
func (r *Runner) TriggerAll() {}

// Runner is returned by Start when callers need a handle for SIGHUP.
type Runner struct{}
