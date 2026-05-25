package health

import (
	"sync"
	"testing"
	"time"

	"bridge/internal/dnscheck"
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

func TestStore_RecordDNSLeakAndClear(t *testing.T) {
	s := NewStore()
	s.SetDomain("smith", "smith.ts.example.com")
	s.RecordDNSResult(dnscheck.Result{
		Domain:    "smith.ts.example.com",
		Resolver:  "8.8.8.8:53",
		When:      time.Unix(1_700_000_000, 0),
		Answers:   []string{"203.0.113.42"},
		Violation: true,
	})
	got, _ := s.Get("smith")
	if got.DNSLeak == nil {
		t.Fatal("expected leak recorded")
	}
	if got.DNSLeak.Domain != "smith.ts.example.com" {
		t.Errorf("domain: %s", got.DNSLeak.Domain)
	}

	// Healthy follow-up on the same probe target clears the leak.
	s.RecordDNSResult(dnscheck.Result{
		Domain:    "smith.ts.example.com",
		Resolver:  "8.8.8.8:53",
		When:      time.Unix(1_700_000_001, 0),
		Answers:   nil,
		Violation: false,
	})
	got, _ = s.Get("smith")
	if got.DNSLeak != nil {
		t.Errorf("leak should be cleared: %+v", got.DNSLeak)
	}
}

func TestStore_RecordDNSResult_UnmappedDomainIgnored(t *testing.T) {
	s := NewStore()
	s.RecordDNSResult(dnscheck.Result{Domain: "anything.example.com", Violation: true, Answers: []string{"1.2.3.4"}})
	if len(s.All()) != 0 {
		t.Errorf("unmapped domain should not create entries: %+v", s.All())
	}
}

func TestStore_CommunityIDForHost(t *testing.T) {
	s := NewStore()
	s.SetDomain("smith", "smith.ts.example.com")
	s.SetDomain("austin", "austin.ts.example.com")

	cases := []struct {
		host string
		want string
	}{
		{"wiki.smith.ts.example.com", "smith"},
		{"git.austin.ts.example.com", "austin"},
		{"smith.ts.example.com", "smith"},        // apex
		{"deep.nested.smith.ts.example.com", ""}, // two labels above
		{"bad.example.com", ""},                  // unknown
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			if got := s.CommunityIDForHost(tc.host); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
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
	s.SetDomain("a", "a.ts.example.com")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); s.Set("a", Snapshot{ETag: "x"}) }()
		go func() { defer wg.Done(); _, _ = s.Get("a"); _ = s.All() }()
		go func() {
			defer wg.Done()
			s.RecordDNSResult(dnscheck.Result{Domain: "a.ts.example.com", Violation: false})
		}()
	}
	wg.Wait()
}
