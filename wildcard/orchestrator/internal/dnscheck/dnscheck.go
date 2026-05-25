// Package dnscheck enforces the SPEC §3.5 invariant at runtime: no name
// under `ts.<basedomain>` ever resolves on the public internet. It does
// so by periodically querying the host's public resolver (bypassing
// Tailscale Split DNS) for each community's domain; any positive answer
// is logged loudly AND surfaced on /__bridge_status.
//
// This does NOT shut the bridge down. If the operator misconfigures
// their public DNS so that `ts.example.com` records leak, the bridge
// keeps working but the trust model is compromised — the user (and the
// admin) need a clear signal, not silence.
//
// Status: STUB.
package dnscheck

import (
	"context"
	"errors"
	"net"
	"time"
)

// Result is the outcome of one check round for one domain.
type Result struct {
	Domain    string
	When      time.Time
	Resolver  string   // e.g. "8.8.8.8:53"; the public resolver actually queried
	Answers   []net.IP // non-empty IFF this is a violation
	Err       error    // network error, if any
	Violation bool     // Answers != nil
}

// Checker periodically queries `domains` against `resolver`.
type Checker struct {
	Domains  []string
	Interval time.Duration
	Resolver string // host:port; e.g. "8.8.8.8:53"

	// OnResult is invoked for every check (violation or not) so the
	// status server can keep an up-to-date snapshot.
	OnResult func(Result)
}

// Run blocks until ctx is cancelled. It does one immediate round and
// then one round per Interval.
//
// Implementation:
//   - Build a net.Resolver that explicitly DialContexts c.Resolver,
//     bypassing /etc/resolv.conf (which inside the container is
//     Tailscale's split-DNS-aware resolver).
//   - For each domain, query both A and AAAA; treat any non-empty
//     answer as a violation.
//   - On NXDOMAIN: not a violation; that's the expected state.
//   - On network error: not a violation (don't false-positive on
//     transient outages); record .Err so the status server can show it.
func (c *Checker) Run(ctx context.Context) error {
	// TODO(impl): see above.
	_ = ctx
	return errNotImplemented
}

var errNotImplemented = errors.New("dnscheck: not yet implemented")
