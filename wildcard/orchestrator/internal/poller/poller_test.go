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
	"bridge/internal/cert"
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
	return &fakeFetcher{calls: map[string]int{}, script: map[string]func(int) (*directory.FetchResult, error){}}
}

func (f *fakeFetcher) Fetch(_ context.Context, id, _, _, _ string) (*directory.FetchResult, error) {
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

// fakeCertLoader returns a synthetic Bundle whose ContentHash flips on demand.
type fakeCertLoader struct {
	mu      sync.Mutex
	bundles map[string]*cert.Bundle
	err     map[string]error
}

func newFakeCertLoader() *fakeCertLoader {
	return &fakeCertLoader{
		bundles: map[string]*cert.Bundle{},
		err:     map[string]error{},
	}
}

func (l *fakeCertLoader) Load(cp, _ string) (*cert.Bundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Key by certPath for community independence.
	if e := l.err[cp]; e != nil {
		return nil, e
	}
	b := l.bundles[cp]
	if b == nil {
		return nil, errors.New("no bundle scripted")
	}
	return b, nil
}

func (l *fakeCertLoader) set(certPath string, b *cert.Bundle) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bundles[certPath] = b
}

func (l *fakeCertLoader) setErr(certPath string, e error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err[certPath] = e
}

func validBundle(hash byte, far time.Time) *cert.Bundle {
	var h [32]byte
	h[0] = hash
	// Construct a bundle that Validate() will accept ONLY if we also
	// provide a real chain — we use a stub Build that ignores Validate.
	return &cert.Bundle{
		CertPath:    "/x",
		KeyPath:     "/y",
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    far,
		DNSNames:    []string{"*.smith.ts.example.com"},
		ContentHash: h,
	}
}

func validDir(domain, name string) *directory.Directory {
	return &directory.Directory{
		Version:   1,
		Community: directory.Community{Name: name, Domain: domain, Tailnet: "smith.ts.net"},
		Services: []directory.Service{
			{Name: name, UpstreamTailnetHost: name + ".smith.ts.net", UpstreamPort: 443},
		},
	}
}

// fakeApplier records applied configs.
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

// stubBuild is a deterministic Build that does NOT call cert.Validate
// (we substitute it because our fake bundles don't have real chains).
// It produces output that varies with directory.ETag-equivalent hash.
func stubBuild(in caddyconfig.Input) ([]byte, error) {
	// Encode a tiny canonical summary covering dirs + certs presence.
	var s string
	for _, c := range in.Config.Communities {
		s += "c=" + c.ID + ";"
		if d, ok := in.Directories[c.ID]; ok {
			for _, svc := range d.Services {
				s += "s=" + svc.Name + ";"
			}
		}
		if b, ok := in.Certs[c.ID]; ok {
			s += "h=" + string([]byte{b.ContentHash[0] + 0x40}) + ";"
		}
	}
	return []byte(s), nil
}

func newRunner(t *testing.T, cfg *config.Config, fet *fakeFetcher, app *fakeApplier, ld *fakeCertLoader) *Runner {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	r := &Runner{
		deps:          Deps{Config: cfg, Health: health.NewStore(), Admin: &adminclient.Client{}, Fetcher: fet},
		apply:         app,
		build:         stubBuild,
		loadc:         ld,
		verify:        func(*cert.Bundle, string, time.Time) error { return nil },
		certs:         map[string]*cert.Bundle{},
		triggers:      map[string]chan struct{}{},
		certTriggerCh: make(chan struct{}, 1),
		logger:        logger,
	}
	for _, c := range cfg.Communities {
		r.triggers[c.ID] = make(chan struct{}, 1)
		r.deps.Health.SetDomain(c.ID, c.Domain)
	}
	// Force cert state directly (bypassing Validate which would reject
	// our synthetic self-signed bundles).
	for _, c := range cfg.Communities {
		if b, err := ld.Load(c.CertPath, c.KeyPath); err == nil {
			r.certs[c.ID] = b
		}
	}
	return r
}

func sampleCfgOne() *config.Config {
	return &config.Config{
		Personal:              config.Personal{AuthKey: "pk", BridgeHostname: "bridge"},
		Communities:           []config.Community{{ID: "smith", Domain: "smith.ts.example.com", AuthKey: "sk", CertPath: "/c1", KeyPath: "/k1"}},
		PollInterval:          time.Minute,
		CertCheckInterval:     time.Minute,
		StateDir:              "/tmp",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
}

func TestPollOnce_SuccessAppliesOnce(t *testing.T) {
	cfg := sampleCfgOne()
	fet := newFakeFetcher()
	fet.script["smith"] = func(int) (*directory.FetchResult, error) {
		return &directory.FetchResult{Directory: validDir("smith.ts.example.com", "wiki"), ETag: `"v1"`}, nil
	}
	app := &fakeApplier{}
	ld := newFakeCertLoader()
	ld.set("/c1", validBundle(1, time.Now().Add(72*time.Hour)))
	r := newRunner(t, cfg, fet, app, ld)

	r.pollOnce(context.Background(), cfg.Communities[0])
	if got, _ := r.deps.Health.Get("smith"); got.CurrentDirectory == nil || got.ETag != `"v1"` {
		t.Fatalf("health after poll: %+v", got)
	}
	if app.count() != 1 {
		t.Fatalf("expected 1 apply, got %d", app.count())
	}

	// Same directory next poll → no apply.
	r.pollOnce(context.Background(), cfg.Communities[0])
	if app.count() != 1 {
		t.Fatalf("dedupe broken: %d applies", app.count())
	}
}

func TestPollOnce_NotModifiedKeepsDirectoryNoApply(t *testing.T) {
	cfg := sampleCfgOne()
	fet := newFakeFetcher()
	var step int32
	fet.script["smith"] = func(int) (*directory.FetchResult, error) {
		s := atomic.AddInt32(&step, 1)
		if s == 1 {
			return &directory.FetchResult{Directory: validDir("smith.ts.example.com", "wiki"), ETag: `"v1"`}, nil
		}
		return &directory.FetchResult{NotModified: true, ETag: `"v1"`}, nil
	}
	app := &fakeApplier{}
	ld := newFakeCertLoader()
	ld.set("/c1", validBundle(1, time.Now().Add(72*time.Hour)))
	r := newRunner(t, cfg, fet, app, ld)

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
	cfg := sampleCfgOne()
	fet := newFakeFetcher()
	var step int32
	fet.script["smith"] = func(int) (*directory.FetchResult, error) {
		s := atomic.AddInt32(&step, 1)
		if s == 1 {
			return &directory.FetchResult{Directory: validDir("smith.ts.example.com", "wiki"), ETag: `"v1"`}, nil
		}
		return nil, errors.New("boom")
	}
	ld := newFakeCertLoader()
	ld.set("/c1", validBundle(1, time.Now().Add(72*time.Hour)))
	r := newRunner(t, cfg, fet, &fakeApplier{}, ld)
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
	cfg := sampleCfgOne()
	fet := newFakeFetcher()
	fet.script["smith"] = func(int) (*directory.FetchResult, error) {
		return &directory.FetchResult{Directory: validDir("smith.ts.example.com", "wiki"), ETag: `"v1"`}, nil
	}
	app := &fakeApplier{failOnce: errors.New("caddy hated it")}
	ld := newFakeCertLoader()
	ld.set("/c1", validBundle(1, time.Now().Add(72*time.Hour)))
	r := newRunner(t, cfg, fet, app, ld)

	r.pollOnce(context.Background(), cfg.Communities[0])
	r.pollOnce(context.Background(), cfg.Communities[0])
	if app.count() != 1 {
		t.Errorf("expected one apply after retry, got %d", app.count())
	}
}

func TestCheckCerts_DetectsRotation(t *testing.T) {
	cfg := sampleCfgOne()
	ld := newFakeCertLoader()
	ld.set("/c1", validBundle(1, time.Now().Add(72*time.Hour)))
	r := newRunner(t, cfg, newFakeFetcher(), &fakeApplier{}, ld)

	// Initial hash known via the seeded r.certs[smith] in newRunner.
	if !r.checkCerts(context.Background()) {
		// First check after seeded value may register as "changed" if seeded prev differs;
		// our seed already wrote /c1's value, so no change expected.
	}
	// Rotate the bundle (new hash byte).
	ld.set("/c1", validBundle(2, time.Now().Add(72*time.Hour)))
	if changed := r.checkCerts(context.Background()); !changed {
		t.Error("expected change detected after rotation")
	}
	r.certsMu.RLock()
	got := r.certs["smith"]
	r.certsMu.RUnlock()
	if got == nil || got.ContentHash[0] != 2 {
		t.Errorf("cert not updated: %+v", got)
	}
}
