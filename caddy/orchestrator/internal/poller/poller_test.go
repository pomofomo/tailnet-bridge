package poller

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bridge/internal/adminclient"
	"bridge/internal/caddyconfig"
	"bridge/internal/config"
	"bridge/internal/directory"
	"bridge/internal/health"
)

// fakeFetcher returns scripted FetchResults per community.
type fakeFetcher struct {
	mu     sync.Mutex
	calls  map[string]int
	script map[string]func(int) (*directory.FetchResult, error)
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{
		calls:  map[string]int{},
		script: map[string]func(int) (*directory.FetchResult, error){},
	}
}

func (f *fakeFetcher) Fetch(ctx context.Context, id, url, prevETag string) (*directory.FetchResult, error) {
	f.mu.Lock()
	n := f.calls[id]
	f.calls[id] = n + 1
	fn := f.script[id]
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("no script")
	}
	return fn(n)
}
func (f *fakeFetcher) Ready(string) (bool, error) { return true, nil }
func (f *fakeFetcher) Close() error               { return nil }

func (f *fakeFetcher) callCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func validDir(prefix, name string) *directory.Directory {
	return &directory.Directory{
		Version:   1,
		Community: directory.Community{Name: prefix, Tailnet: prefix + ".ts.net", Prefix: prefix + "-"},
		Services: []directory.Service{
			{Name: name, UpstreamHost: name + "." + prefix + ".ts.net", UpstreamPort: 443, UpstreamScheme: "https"},
		},
	}
}

// fakeApplier records applied configs and counts.
type fakeApplier struct {
	mu       sync.Mutex
	applied  [][]byte
	failOnce error
}

func (a *fakeApplier) Apply(_ context.Context, j []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failOnce != nil {
		err := a.failOnce
		a.failOnce = nil
		return err
	}
	cp := append([]byte(nil), j...)
	a.applied = append(a.applied, cp)
	return nil
}
func (a *fakeApplier) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.applied)
}

func newRunner(t *testing.T, cfg *config.Config, fet *fakeFetcher, app *fakeApplier) *Runner {
	t.Helper()
	build := caddyconfig.Build
	logger := log.New(io.Discard, "", 0)
	r := &Runner{
		deps:     Deps{Config: cfg, Health: health.NewStore(), Admin: &adminclient.Client{}, Fetcher: fet, Build: build},
		apply:    app,
		build:    build,
		triggers: map[string]chan struct{}{},
		logger:   logger,
	}
	for _, c := range cfg.Communities {
		r.triggers[c.ID] = make(chan struct{}, 1)
	}
	return r
}

func TestPollOnce_SuccessUpdatesHealthAndAppliesOnce(t *testing.T) {
	cfg := &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", AuthKey: "sk"}},
		PollInterval:          time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
	fet := newFakeFetcher()
	fet.script["smith"] = func(n int) (*directory.FetchResult, error) {
		return &directory.FetchResult{Directory: validDir("smith", "wiki"), ETag: `"v1"`}, nil
	}
	app := &fakeApplier{}
	r := newRunner(t, cfg, fet, app)

	r.pollOnce(context.Background(), cfg.Communities[0])
	if got, _ := r.deps.Health.Get("smith"); got.CurrentDirectory == nil || got.ETag != `"v1"` {
		t.Fatalf("health after poll: %+v", got)
	}
	if app.count() != 1 {
		t.Fatalf("expected 1 apply, got %d", app.count())
	}

	// Same directory next time → no apply (hash unchanged).
	r.pollOnce(context.Background(), cfg.Communities[0])
	if app.count() != 1 {
		t.Fatalf("dedupe broken: %d applies", app.count())
	}
}

func TestPollOnce_NotModifiedKeepsDirectory(t *testing.T) {
	cfg := &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", AuthKey: "sk"}},
		PollInterval:          time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
	fet := newFakeFetcher()
	var step int32
	fet.script["smith"] = func(n int) (*directory.FetchResult, error) {
		s := atomic.AddInt32(&step, 1)
		if s == 1 {
			return &directory.FetchResult{Directory: validDir("smith", "wiki"), ETag: `"v1"`}, nil
		}
		return &directory.FetchResult{NotModified: true, ETag: `"v1"`}, nil
	}
	app := &fakeApplier{}
	r := newRunner(t, cfg, fet, app)

	r.pollOnce(context.Background(), cfg.Communities[0])
	r.pollOnce(context.Background(), cfg.Communities[0])

	snap, _ := r.deps.Health.Get("smith")
	if snap.CurrentDirectory == nil {
		t.Fatal("directory lost after 304")
	}
	if app.count() != 1 {
		t.Errorf("304 should not re-apply: %d", app.count())
	}
}

func TestPollOnce_FetchErrorPreservesPriorDirectory(t *testing.T) {
	cfg := &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", AuthKey: "sk"}},
		PollInterval:          time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
	fet := newFakeFetcher()
	var step int32
	fet.script["smith"] = func(n int) (*directory.FetchResult, error) {
		s := atomic.AddInt32(&step, 1)
		if s == 1 {
			return &directory.FetchResult{Directory: validDir("smith", "wiki"), ETag: `"v1"`}, nil
		}
		return nil, errors.New("boom")
	}
	r := newRunner(t, cfg, fet, &fakeApplier{})
	r.pollOnce(context.Background(), cfg.Communities[0])
	r.pollOnce(context.Background(), cfg.Communities[0])

	snap, _ := r.deps.Health.Get("smith")
	if snap.CurrentDirectory == nil {
		t.Fatal("directory dropped on error")
	}
	if snap.LastError != "boom" {
		t.Errorf("expected error: %+v", snap)
	}
}

func TestPollOnce_ApplyFailureLeavesHashUnchanged(t *testing.T) {
	cfg := &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", AuthKey: "sk"}},
		PollInterval:          time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
	fet := newFakeFetcher()
	fet.script["smith"] = func(n int) (*directory.FetchResult, error) {
		return &directory.FetchResult{Directory: validDir("smith", "wiki"), ETag: `"v1"`}, nil
	}
	app := &fakeApplier{failOnce: errors.New("caddy hated it")}
	r := newRunner(t, cfg, fet, app)

	r.pollOnce(context.Background(), cfg.Communities[0])
	// Applier rejected; next poll must re-attempt apply with identical
	// content (hash bookkeeping must not be advanced on failure).
	r.pollOnce(context.Background(), cfg.Communities[0])
	if app.count() != 1 {
		t.Errorf("expected 1 successful apply after a retry, got %d", app.count())
	}
}

// recordingSink captures the prefix map set by the runner.
type recordingSink struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *recordingSink) SetPrefixMap(m map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = m
}

func TestPollOnce_PopulatesPrefixSink(t *testing.T) {
	cfg := &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", AuthKey: "sk"}},
		PollInterval:          time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
	fet := newFakeFetcher()
	fet.script["smith"] = func(n int) (*directory.FetchResult, error) {
		return &directory.FetchResult{Directory: validDir("smith", "wiki"), ETag: `"v1"`}, nil
	}
	sink := &recordingSink{}
	r := newRunner(t, cfg, fet, &fakeApplier{})
	r.deps.Sink = sink
	r.pollOnce(context.Background(), cfg.Communities[0])

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.m["smith-"] != "smith" {
		t.Errorf("prefix map: %+v", sink.m)
	}
}
