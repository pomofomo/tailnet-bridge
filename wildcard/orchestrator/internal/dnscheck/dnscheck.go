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
package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

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
	// "smithfamily.ts.example.com"). For each, we also probe the
	// canonical synthetic "any" name "any.<domain>" since the
	// wildcard-style violation could manifest only on subdomains.
	Domains []string

	// Resolver is host:port of the public resolver to query
	// (e.g. "8.8.8.8:53"). Bypasses /etc/resolv.conf.
	Resolver string

	// Interval is how often to run a full pass. <=0 means single-shot.
	Interval time.Duration

	// OnResult is invoked for every check (violation or not). The
	// status server uses this to keep an up-to-date snapshot.
	OnResult func(Result)
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
	var wg sync.WaitGroup
	for _, d := range c.Domains {
		wg.Add(2)
		// Probe BOTH the domain itself and a synthetic subdomain — the
		// wildcard cert covers *.<domain>, and a public-DNS leak might
		// only manifest on one or the other depending on how the
		// operator misconfigured records.
		go func(dom string) {
			defer wg.Done()
			res := probe(ctx, r, c.Resolver, dom)
			if c.OnResult != nil {
				c.OnResult(res)
			}
		}(d)
		go func(dom string) {
			defer wg.Done()
			res := probe(ctx, r, c.Resolver, "any."+dom)
			if c.OnResult != nil {
				c.OnResult(res)
			}
		}(d)
	}
	wg.Wait()
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
