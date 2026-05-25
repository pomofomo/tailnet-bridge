package health

import (
	"sync"
	"testing"
	"time"
)

func TestStore_BasicSetGet(t *testing.T) {
	s := NewStore()
	now := time.Unix(1_700_000_000, 0)
	s.Set("smith", Snapshot{LastSuccessfulPoll: now, ETag: `"v1"`})
	got, ok := s.Get("smith")
	if !ok {
		t.Fatal("expected entry")
	}
	if got.ETag != `"v1"` || !got.LastSuccessfulPoll.Equal(now) {
		t.Errorf("got %+v", got)
	}
	if _, ok := s.Get("missing"); ok {
		t.Error("unexpected missing entry")
	}
}

func TestStore_UpdateRetainsPriorFields(t *testing.T) {
	s := NewStore()
	s.Set("smith", Snapshot{ETag: `"v1"`})
	s.Update("smith", func(snap Snapshot) Snapshot {
		snap.LastError = "boom"
		return snap
	})
	got, _ := s.Get("smith")
	if got.ETag != `"v1"` {
		t.Errorf("etag lost: %+v", got)
	}
	if got.LastError != "boom" {
		t.Errorf("error not set: %+v", got)
	}
}

func TestStore_AllSnapshot(t *testing.T) {
	s := NewStore()
	s.Set("a", Snapshot{LastError: "x"})
	s.Set("b", Snapshot{LastError: "y"})
	m := s.All()
	if len(m) != 2 || m["a"].LastError != "x" || m["b"].LastError != "y" {
		t.Errorf("All: %+v", m)
	}
}

func TestStore_Concurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Set("a", Snapshot{ETag: "x"})
		}()
		go func() {
			defer wg.Done()
			_, _ = s.Get("a")
			_ = s.All()
		}()
	}
	wg.Wait()
}
