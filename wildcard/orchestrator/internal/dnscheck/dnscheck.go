// Package dnscheck enforces the SPEC §3.5 / §9.5 invariant at runtime:
// no name under `ts.<basedomain>` ever resolves on the public internet.
// It does so by periodically querying a configured public resolver
// (bypassing Tailscale Split DNS) for each community's domain. Any
// positive answer is reported as a violation.
//
// This does NOT shut the bridge down. If the operator misconfigures
// their public DNS so that ts.<base> records leak, the bridge keeps
// working but the trust model is compromised — the user (and the admin)
// need a clear signal, not silence.
//
// Logging dedupe: every probe fires `OnResult` (for the live status
// snapshot), but `OnTransition` is only invoked when a domain crosses
// the healthy ↔ violating boundary. Operators get one log line per
// state change instead of two per domain per poll_interval (SPEC §12.4).
package dnscheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// canaryPrefix is prepended to a random label per Checker instance to
// probe a wildcard-style leak. Picking a unique name avoids colliding
// with any service a community might legitimately operate (a literal
// "any.<domain>" would).
const canaryPrefix = "bridge-canary-"

// Result is the outcome of one check round for one domain.
type Result struct {
	Domain    string
	When      time.Time
	Resolver  string // host:port of the public resolver queried
	Answers   []string
	Err       error // network error, if any
	Violation bool  // Answers != nil && Err == nil
}

// Checker periodically queries `Domains` against `Resolver`.
type Checker struct {
	// Domains are the community subdomains to verify (e.g.
	// "smithfamily.ts.example.com"). For each, we also probe a
	// per-process canary label "bridge-canary-<rand>.<domain>" so a
	// wildcard public DNS misconfig surfaces too.
	Domains []string

	// Resolver is host:port of the public resolver to query
	// (e.g. "8.8.8.8:53"). Bypasses /etc/resolv.conf.
	Resolver string

	// Interval is how often to run a full pass. <=0 means single-shot.
	Interval time.Duration

	// OnResult is invoked for every probe outcome. The status server
	// uses this to keep an up-to-date snapshot.
	OnResult func(Result)

	// OnTransition is invoked the first time a domain crosses into
	// (violating=true) or out of (violating=false) the leak state,
	// regardless of the probe label. Designed for logging: one line
	// per real change instead of N per tick.
	OnTransition func(domain string, violating bool, answers []string, resolver string)

	// canary is a per-Checker random subdomain probed alongside the
	// apex; lazily generated on first Run / CheckOnce.
	canaryMu sync.Mutex
	canary   string

	// transition state, per-domain. Lazily allocated.
	stateMu sync.Mutex
	state   map[string]bool // domain → currently-violating
}

// Run blocks until ctx is cancelled. It runs one immediate pass and then
// one pass per Interval.
func (c *Checker) Run(ctx context.Context) error {
	if c.Resolver == "" {
		return fmt.Errorf("dnscheck: empty resolver")
	}
	r := buildResolver(c.Resolver)

	c.runOnce(ctx, r)

	if c.Interval <= 0 {
		return nil
	}
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			c.runOnce(ctx, r)
		}
	}
}

// CheckOnce probes a single domain and returns the Result. Exported
// for one-shot tooling and tests.
func (c *Checker) CheckOnce(ctx context.Context, domain string) Result {
	if c.Resolver == "" {
		return Result{Domain: domain, When: time.Now(), Err: fmt.Errorf("dnscheck: empty resolver")}
	}
	return probe(ctx, buildResolver(c.Resolver), c.Resolver, domain)
}

func (c *Checker) runOnce(ctx context.Context, r *net.Resolver) {
	canary := c.canaryLabel()
	var wg sync.WaitGroup
	for _, d := range c.Domains {
		wg.Add(2)
		go func(dom string) {
			defer wg.Done()
			c.dispatch(probe(ctx, r, c.Resolver, dom))
		}(d)
		go func(dom string) {
			defer wg.Done()
			c.dispatch(probe(ctx, r, c.Resolver, canary+"."+dom))
		}(d)
	}
	wg.Wait()
}

// dispatch routes one Result to OnResult and, on edge transitions,
// OnTransition.
func (c *Checker) dispatch(res Result) {
	if c.OnResult != nil {
		c.OnResult(res)
	}
	if c.OnTransition == nil {
		return
	}
	// Only positive answers count; errors don't flip transition state.
	if res.Err != nil {
		return
	}
	c.stateMu.Lock()
	if c.state == nil {
		c.state = make(map[string]bool)
	}
	prev := c.state[res.Domain]
	c.state[res.Domain] = res.Violation
	c.stateMu.Unlock()
	if prev != res.Violation {
		c.OnTransition(res.Domain, res.Violation, res.Answers, res.Resolver)
	}
}

func (c *Checker) canaryLabel() string {
	c.canaryMu.Lock()
	defer c.canaryMu.Unlock()
	if c.canary != "" {
		return c.canary
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a stable label; collision risk is acceptable.
		c.canary = canaryPrefix + "fallback"
	} else {
		c.canary = canaryPrefix + hex.EncodeToString(b[:])
	}
	return c.canary
}

func buildResolver(addr string) *net.Resolver {
	d := net.Dialer{Timeout: 5 * time.Second}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// Always dial the configured resolver regardless of what
			// the system thinks the nameserver is. UDP first; TCP
			// fallback handled by the Go resolver itself.
			return d.DialContext(ctx, "udp", addr)
		},
	}
}

func probe(ctx context.Context, r *net.Resolver, resolverAddr, domain string) Result {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.LookupHost(cctx, domain)
	res := Result{Domain: domain, When: time.Now(), Resolver: resolverAddr}
	if err != nil {
		// NXDOMAIN / no-such-host is the expected, healthy state.
		// We only flag *positive* answers; errors are recorded so the
		// status server can show "we're checking, no leak detected".
		var de *net.DNSError
		if errors.As(err, &de) && (de.IsNotFound || de.IsTemporary) {
			return res
		}
		res.Err = err
		return res
	}
	res.Answers = ips
	res.Violation = len(ips) > 0
	return res
}
