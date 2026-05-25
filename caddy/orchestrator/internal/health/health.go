// Package health tracks per-community polling state (SPEC §9.3).
//
// Shared by poller (writer) and status (reader); all access is
// goroutine-safe.
package health

import (
	"sync"
	"time"

	"bridge/internal/directory"
)

// Snapshot is the read-only view served on /__bridge_status and used to
// render /__bridge_error.
type Snapshot struct {
	LastSuccessfulPoll time.Time            `json:"last_successful_poll,omitempty"`
	LastError          string               `json:"last_error,omitempty"`
	CurrentDirectory   *directory.Directory `json:"current_directory,omitempty"`
	ETag               string               `json:"etag,omitempty"`
}

// Store is a concurrent map keyed by community ID.
type Store struct {
	mu   sync.RWMutex
	data map[string]Snapshot
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{data: make(map[string]Snapshot)}
}

// Set replaces the snapshot for one community.
func (s *Store) Set(communityID string, snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[communityID] = snap
}

// Update mutates the snapshot for one community via fn, holding the
// write lock for the duration. Useful for "update error, keep prior
// directory" semantics.
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
