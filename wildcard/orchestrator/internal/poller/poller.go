// Package poller owns the bridge's steady-state event loop:
//
//   - per-community goroutines that re-fetch the directory every
//     poll_interval,
//   - a cert.Watcher reacting to file rotations on cert_check_interval,
//   - a SIGHUP fan-out that triggers immediate re-poll + re-stat,
//   - a single config-regen path serialized by a mutex so that
//     concurrent triggers coalesce into one POST to Caddy.
//
// Status: STUB.
package poller

import (
	"context"
	"errors"
	"net/http"

	"bridge/internal/adminclient"
	"bridge/internal/caddyconfig"
	"bridge/internal/cert"
	"bridge/internal/config"
	"bridge/internal/directory"
	"bridge/internal/health"
)

// Deps bundles the dependencies the poller needs. Each field is an
// interface or a concrete value so tests can substitute fakes.
type Deps struct {
	Cfg    *config.Config
	Health *health.Tracker

	// CommunityClient returns an HTTP client wired to dial via the
	// community-specific ephemeral tsnet node. The poller calls this
	// every time it polls (so the tsnet node can be torn down between
	// polls if desired) or once at startup (caller's choice).
	CommunityClient func(ctx context.Context, c config.Community) (*http.Client, error)

	// LoadCert resolves a community's PEM files into a cert.Bundle and
	// validates them. Wrapping cert.Load + cert.Validate lets tests
	// inject synthetic bundles.
	LoadCert func(c config.Community) (*cert.Bundle, error)

	// Admin is the live Caddy admin client used to POST regenerated config.
	Admin adminclient.Client

	// BuildConfig wraps caddyconfig.Build. Tests can substitute it.
	BuildConfig func(caddyconfig.Inputs) ([]byte, error)
}

// Run blocks until ctx is done. It owns:
//   - one goroutine per community polling its directory,
//   - one cert.Watcher goroutine,
//   - one SIGHUP handler (the caller wires os/signal),
//   - one merger goroutine that, on any change, regenerates the Caddy
//     config and POSTs it.
//
// On every successful poll/cert reload, the merger:
//  1. Builds caddyconfig.Inputs from the latest snapshots.
//  2. Computes sha256(jsonOut). If unchanged, no POST.
//  3. Otherwise, adminclient.Load(ctx, addr, jsonOut). On non-2xx,
//     keeps the previous config live, records the error in health,
//     and surfaces it on /__bridge_status.
//
// On SIGHUP (delivered via Trigger), the poller does an immediate
// fan-out: every community gets re-polled, every cert pair gets
// re-stat-and-loaded, then a single merger pass runs.
func Run(ctx context.Context, d Deps) error {
	// TODO(impl): see the doc comment.
	_ = d
	<-ctx.Done()
	return ctx.Err()
}

// Trigger asks the running poller to do an immediate full re-poll.
// Safe to call from a signal handler. A no-op if the poller isn't
// running.
func (h *Handle) Trigger() {
	// TODO(impl): non-blocking send to an internal channel.
}

// Handle is returned by Start so callers can Trigger() without exposing
// internals. The Handle is valid for the lifetime of the Run goroutine.
type Handle struct {
	trigger chan struct{}
}

// Start runs the poller on a fresh goroutine and returns a Handle for
// signalling. Equivalent to Run but doesn't block.
func Start(ctx context.Context, d Deps) (*Handle, <-chan error) {
	errCh := make(chan error, 1)
	h := &Handle{trigger: make(chan struct{}, 1)}
	go func() {
		errCh <- Run(ctx, d)
	}()
	// TODO(impl): wire the channel into Run so Trigger has effect.
	return h, errCh
}

// FetchOnce is exported for tests and one-shot diagnostic tools. It runs
// the full poll-and-merge cycle exactly once and returns the resulting
// JSON config (without POSTing). Wraps directory.Fetch, cert.Load+Validate,
// caddyconfig.Build.
func FetchOnce(ctx context.Context, d Deps) ([]byte, error) {
	// TODO(impl):
	//   for each community in d.Cfg.Communities:
	//     - client, err := d.CommunityClient(ctx, c)
	//     - dir, etag, status, err := directory.Fetch(...)
	//     - bundle, err := d.LoadCert(c)
	//     - record into d.Health
	//   inputs := caddyconfig.Inputs{...}
	//   return d.BuildConfig(inputs)
	_, _ = ctx, d
	_ = directory.Directory{} // keep import live during stub phase
	return nil, errNotImplemented
}

var errNotImplemented = errors.New("poller: not yet implemented")
