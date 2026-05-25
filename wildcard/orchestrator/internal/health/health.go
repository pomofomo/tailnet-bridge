// Package health tracks per-community polling, cert, and DNS-leak
// state (SPEC §10.4, §12).
//
// Shared by poller, cert watcher, dnscheck (writers) and status
// (reader); all access is goroutine-safe.
package health

import (
	"strings"
	"sync"
	"time"

	"bridge/internal/directory"
	"bridge/internal/dnscheck"
)

// Snapshot is the read-only view served on /__bridge_status and used to
// render /__bridge_error. JSON tags are designed to be stable for
// scripted consumers.
type Snapshot struct {
	LastSuccessfulPoll time.Time            `json:"last_successful_poll,omitempty"`
	LastPollAttempt    time.Time            `json:"last_poll_attempt,omitempty"`
	LastError          string               `json:"last_error,omitempty"`
	ETag               string               `json:"etag,omitempty"`
	CurrentDirectory   *directory.Directory `json:"current_directory,omitempty"`

	// Cert state, populated by the cert watcher.
	CertNotBefore  time.Time `json:"cert_not_before,omitempty"`
	CertNotAfter   time.Time `json:"cert_not_after,omitempty"`
	CertLastReload time.Time `json:"cert_last_reload,omitempty"`
	CertError      string    `json:"cert_error,omitempty"`

	// DNSLeak holds the most recent dnscheck violation for this
	// community's domain (or a probed subdomain). nil means no
	// current leak.
	DNSLeak *DNSLeak `json:"dns_leak,omitempty"`
}

// DNSLeak summarises a public-DNS violation.
type DNSLeak struct {
	Domain   string    `json:"domain"`
	Resolver string    `json:"resolver"`
	When     time.Time `json:"when"`
	Answers  []string  `json:"answers"`
}

// Store is a concurrent map keyed by community ID.
type Store struct {
	mu   sync.RWMutex
	data map[string]Snapshot

	// domains is the set of community.id ↔ community.domain mappings
	// used to attribute dnscheck Results back to a community. The
	// poller calls SetDomain on every successful Config load.
	domains map[string]string // id → domain
	// reverse caches the domain → id mapping so error-page renders
	// and dnscheck dispatch don't walk `domains` each time.
	reverse map[string]string
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		data:    make(map[string]Snapshot),
		domains: make(map[string]string),
		reverse: make(map[string]string),
	}
}

// SetDomain registers a community's primary domain. Used by dnscheck
// result routing and host → community-id lookup. Idempotent.
func (s *Store) SetDomain(communityID, domain string) {
	dom := strings.ToLower(strings.TrimSuffix(domain, "."))
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.domains[communityID]; ok && old != dom {
		delete(s.reverse, old)
	}
	s.domains[communityID] = dom
	s.reverse[dom] = communityID
}

// Set replaces the snapshot for one community.
func (s *Store) Set(communityID string, snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[communityID] = snap
}

// Update mutates the snapshot for one community via fn, holding the
// write lock for the duration.
func (s *Store) Update(communityID string, fn func(Snapshot) Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[communityID] = fn(s.data[communityID])
}

// Get returns the snapshot for communityID. The bool reports whether an
// entry existed; the returned snapshot is otherwise a shallow copy.
func (s *Store) Get(communityID string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.data[communityID]
	return snap, ok
}

// All returns a snapshot of every community.
func (s *Store) All() map[string]Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Snapshot, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// RecordDNSResult routes one dnscheck.Result to the matching community's
// Snapshot. Probes that don't map to a registered domain are ignored.
//
// A Result Domain matches a community when it is either the community
// domain itself or a strict subdomain of it (handles the per-process
// canary label dnscheck appends).
func (s *Store) RecordDNSResult(r dnscheck.Result) {
	probed := strings.ToLower(strings.TrimSuffix(r.Domain, "."))
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := s.matchCommunityLocked(probed)
	if matched == "" {
		return
	}
	snap := s.data[matched]
	if r.Violation {
		snap.DNSLeak = &DNSLeak{
			Domain:   r.Domain,
			Resolver: r.Resolver,
			When:     r.When,
			Answers:  append([]string(nil), r.Answers...),
		}
	} else if r.Err == nil {
		// Healthy result for this probe target. Clear leak only if
		// the snapshot's existing leak was for THIS exact probe — a
		// stale leak on the canary should not be cleared by an apex
		// success.
		if snap.DNSLeak != nil && snap.DNSLeak.Domain == r.Domain {
			snap.DNSLeak = nil
		}
	}
	s.data[matched] = snap
}

// CommunityIDForHost looks up the community ID whose registered domain
// is a suffix of host (with a single label in between — `<svc>.<domain>`).
// Returns "" if no match.
func (s *Store) CommunityIDForHost(host string) string {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Apex match: exact community domain.
	if id, ok := s.reverse[h]; ok {
		return id
	}
	// One-label-down match: "<svc>.<domain>".
	if dot := strings.IndexByte(h, '.'); dot >= 0 {
		parent := h[dot+1:]
		// label before dot must contain no further dots — guaranteed
		// by the IndexByte index, so just look the parent up.
		if id, ok := s.reverse[parent]; ok {
			return id
		}
	}
	return ""
}

// matchCommunityLocked finds the community whose registered domain is
// equal to probed or a parent of probed. Caller must hold s.mu.
func (s *Store) matchCommunityLocked(probed string) string {
	if id, ok := s.reverse[probed]; ok {
		return id
	}
	// Walk parents one label at a time: dnscheck probes the apex and
	// also a canary subdomain like "bridge-canary-xxxx.<domain>".
	rest := probed
	for {
		dot := strings.IndexByte(rest, '.')
		if dot < 0 {
			return ""
		}
		rest = rest[dot+1:]
		if id, ok := s.reverse[rest]; ok {
			return id
		}
	}
}
