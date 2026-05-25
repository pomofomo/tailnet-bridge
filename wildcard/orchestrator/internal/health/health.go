// Package health tracks per-community state shared between the poller
// and the status server. Holds the latest directory, the last successful
// poll timestamp, the last error (if any), the current cert bundle, and
// the most recent public-DNS warning. Concurrency-safe.
//
// Status: STUB.
package health

import (
	"sync"
	"time"

	"bridge/internal/cert"
	"bridge/internal/directory"
	"bridge/internal/dnscheck"
)

// State is the per-community snapshot exposed to /__bridge_status.
type State struct {
	LastSuccessfulPoll time.Time
	LastPollAttempt    time.Time
	LastError          string
	ETag               string
	CurrentDirectory   *directory.Directory

	CertNotAfter   time.Time
	CertNotBefore  time.Time
	CertLastReload time.Time
	CertError      string

	PublicDNSWarning *dnscheck.Result // nil = no current violation
}

// Tracker is a goroutine-safe map of community-id → State.
type Tracker struct {
	mu sync.RWMutex
	m  map[string]*State
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{m: make(map[string]*State)}
}

// RecordPollSuccess updates the directory + bookkeeping fields for a
// community after a successful poll.
func (t *Tracker) RecordPollSuccess(id, etag string, d *directory.Directory) {
	// TODO(impl): lock, upsert, set LastSuccessfulPoll = time.Now().
	_, _, _ = id, etag, d
}

// RecordPollError records a poll failure WITHOUT clearing the previously
// cached directory (SPEC §12 / Caddy spec §10: services from a
// temporarily unreachable community stay listed).
func (t *Tracker) RecordPollError(id string, err error) {
	// TODO(impl): lock, set LastError, LastPollAttempt.
	_, _ = id, err
}

// RecordCert updates the cert fields after a successful Load+Validate.
func (t *Tracker) RecordCert(id string, b *cert.Bundle) {
	// TODO(impl): lock, copy NotBefore/NotAfter, set CertLastReload.
	_, _ = id, b
}

// RecordCertError records a cert load/validate failure.
func (t *Tracker) RecordCertError(id string, err error) {
	// TODO(impl): lock, set CertError.
	_, _ = id, err
}

// RecordDNSResult records the outcome of a dnscheck round.
func (t *Tracker) RecordDNSResult(r dnscheck.Result) {
	// TODO(impl): find the matching community by domain and update its
	// PublicDNSWarning field (set to nil if !r.Violation).
	_ = r
}

// Snapshot returns a deep copy of the current state for the status
// server. Caller may mutate the returned map without affecting Tracker.
func (t *Tracker) Snapshot() map[string]State {
	// TODO(impl): RLock, deep-copy.
	return map[string]State{}
}
