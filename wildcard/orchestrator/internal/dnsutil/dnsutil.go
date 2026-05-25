// Package dnsutil holds the shared "is this a valid bridge domain"
// rules used by both internal/config (parsing config.yml) and
// internal/directory (validating community-published directories).
//
// Keeping the rule in one place means the regex, label-count, and
// "second label must be 'ts'" check (SPEC §3.5, §3.6, §13) cannot
// drift between callers.
package dnsutil

import (
	"fmt"
	"regexp"
	"strings"
)

// DomainRE matches the general DNS-name shape (lowercase + digits +
// hyphens, dot-separated). It does NOT enforce the wildcard-bridge
// "must have a ts label" rule; ValidateBridgeDomain does.
var DomainRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)

// MaxDNSName is the standard ≤253-char total for a DNS name.
const MaxDNSName = 253

// MaxLabel is the per-label ≤63-char DNS limit.
const MaxLabel = 63

// ValidateBridgeDomain enforces the wildcard-bridge community domain
// shape: <community>.ts.<basedomain>, where:
//   - at least 4 labels deep,
//   - lowercase + digits + hyphens per label,
//   - the SECOND label (after the leading community label) is exactly
//     "ts" — this is the tailnet-only zone whose public-DNS invariant
//     grounds the wildcard cert trust model (SPEC §3.5 / §3.6).
//
// Returns nil on success. Errors do NOT wrap; callers prepend their
// own context.
func ValidateBridgeDomain(domain string) error {
	d := strings.ToLower(domain)
	if !DomainRE.MatchString(d) {
		return fmt.Errorf("domain %q is not a valid DNS name", domain)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 4 {
		return fmt.Errorf("domain %q must be at least 4 labels (e.g. community.ts.base.tld)", domain)
	}
	if labels[1] != "ts" {
		return fmt.Errorf("domain %q: second label must be %q (SPEC §3.6 — the tailnet-only zone)", domain, "ts")
	}
	for _, lbl := range labels {
		if len(lbl) > MaxLabel {
			return fmt.Errorf("domain %q: label %q exceeds %d chars", domain, lbl, MaxLabel)
		}
	}
	if len(d) > MaxDNSName {
		return fmt.Errorf("domain %q exceeds %d chars", domain, MaxDNSName)
	}
	return nil
}

// BaseDomain returns everything after the first label of domain.
// "smith.ts.example.com" → "ts.example.com". Returns the input
// unchanged when there is no dot.
func BaseDomain(domain string) string {
	d := strings.ToLower(domain)
	if i := strings.IndexByte(d, '.'); i >= 0 {
		return d[i+1:]
	}
	return d
}
