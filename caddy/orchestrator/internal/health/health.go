// Package health tracks per-community polling state (SPEC §9.3).
//
// Shared by poller (writer) and status (reader).
package health

import (
	"bridge/internal/directory"
	"sync"
	"time"
)

// Snapshot is the read-only view served on /__bridge_status and used to
// render /__bridge_error.
type Snapshot struct {
	LastSuccessfulPoll time.Time          `json:"last_successful_poll"`
	LastError          string             `json:"last_error,omitempty"`
	CurrentDirectory   *directory.Directory `json:"current_directory,omitempty"`
	ETag               string             `json:"etag,omitempty"`
}

// Store is a concurrent map keyed by community ID.
type Store struct {
	mu   sync.RWMutex
	data map[string]Snapshot
}

// NewStore returns an empty Store.
func NewStore() *Store { return &Store{data: map[string]Snapshot{}} }

// Set replaces the snapshot for one community.
func (s *Store) Set(communityID string, snap Snapshot) {}

// Get returns a copy of the snapshot.
func (s *Store) Get(communityID string) (Snapshot, bool) { return Snapshot{}, false }

// All returns a snapshot of every community (used by /__bridge_status).
func (s *Store) All() map[string]Snapshot { return nil }
