// Package poller drives the per-community polling loop (SPEC §9.1, §9.5).
//
// Each community owns one goroutine that fetches its directory through a
// dedicated tsnet node. A single mutex serializes
// (read directories) → (caddyconfig.Build) → (admin /load) so config
// regeneration is atomic. SIGHUP fans out to every community.
package poller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"sync"
	"time"

	"bridge/internal/adminclient"
	"bridge/internal/caddyconfig"
	"bridge/internal/config"
	"bridge/internal/directory"
	"bridge/internal/health"
)

// Fetcher abstracts directory I/O. The production implementation is
// tsnetFetcher; tests substitute a fake.
type Fetcher interface {
	// Fetch performs a single directory GET for one community,
	// honouring prevETag for caching. Implementations MUST block until
	// the underlying tsnet node is online or ctx is cancelled.
	Fetch(ctx context.Context, communityID, url, prevETag string) (*directory.FetchResult, error)
	// Ready reports whether the community's tsnet node has come up at
	// least once. It is consulted at startup to decide whether the
	// per-community goroutine should be spawned.
	Ready(communityID string) (bool, error)
	// Close releases all underlying tsnet servers.
	Close() error
}

// Applier is the side-effect target for a successful regeneration: it
// receives the freshly-merged Caddy JSON. The production wiring sends
// to adminclient.Client; tests inject a recorder.
type Applier interface {
	Apply(ctx context.Context, jsonConfig []byte) error
}

// adminApplier adapts *adminclient.Client to the Applier interface.
type adminApplier struct{ c *adminclient.Client }

func (a adminApplier) Apply(ctx context.Context, j []byte) error { return a.c.Load(ctx, j) }

// PrefixMapSink receives the (prefix → communityID) map after every
// successful regeneration so the status server can resolve a failing
// host back to its community.
type PrefixMapSink interface {
	SetPrefixMap(map[string]string)
}

// Deps is the dependency bundle injected by main.
type Deps struct {
	Config  *config.Config
	Health  *health.Store
	Admin   *adminclient.Client
	Fetcher Fetcher
	// Build defaults to caddyconfig.Build; tests can override.
	Build func(caddyconfig.Input) ([]byte, error)
	// Sink is the status-server prefix-map sink; optional.
	Sink PrefixMapSink
}

// Runner is the live polling loop. Construct via Start.
type Runner struct {
	deps    Deps
	apply   Applier
	build   func(caddyconfig.Input) ([]byte, error)
	regenMu sync.Mutex
	lastHash [32]byte
	haveHash bool

	triggers map[string]chan struct{} // per-community fan-out targets

	logger *log.Logger
}

// Start spawns one goroutine per community and returns a Runner whose
// TriggerAll method the signal handler can call on SIGHUP.
//
// Start blocks until each community's first fetch attempt has been made.
// (This is what gives the orchestrator a usable initial config: §9.1
// step 5.)
func Start(ctx context.Context, deps Deps, logger *log.Logger) (*Runner, error) {
	if deps.Config == nil || deps.Health == nil || deps.Admin == nil || deps.Fetcher == nil {
		return nil, fmt.Errorf("poller: incomplete deps")
	}
	if logger == nil {
		logger = log.Default()
	}
	build := deps.Build
	if build == nil {
		build = caddyconfig.Build
	}
	r := &Runner{
		deps:     deps,
		apply:    adminApplier{c: deps.Admin},
		build:    build,
		triggers: make(map[string]chan struct{}, len(deps.Config.Communities)),
		logger:   logger,
	}

	// Spawn per-community goroutines. We do the first poll synchronously
	// (best-effort) so the first regenerate() call below has data to work
	// with; tsnet readiness failures are recorded in Health.
	var wg sync.WaitGroup
	for _, c := range deps.Config.Communities {
		c := c
		trig := make(chan struct{}, 1)
		r.triggers[c.ID] = trig
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.runCommunity(ctx, c, trig)
		}()
	}

	// Wait for first-poll completion. We approximate this by giving each
	// community CommunityJoinTimeout + a small grace period to write its
	// initial Health snapshot, then return — the loop continues in the
	// background.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	// Block on first-poll watermark: each community's goroutine writes a
	// Health entry as its first action. We wait until every community
	// has either a directory or an error recorded, with a hard ceiling.
	r.waitFirstPoll(ctx, deps.Config.CommunityJoinTimeout+5*time.Second)

	// Do the initial regenerate now so Caddy gets configured even if some
	// communities are still in error.
	if err := r.regenerate(ctx); err != nil {
		// Initial regen failures are logged but don't abort startup —
		// the next successful poll will retry.
		logger.Printf("poller: initial regenerate: %v", err)
	}

	return r, nil
}

// TriggerAll wakes every per-community goroutine. Non-blocking; if a
// goroutine is already busy or has a pending trigger, the extra signal
// coalesces.
func (r *Runner) TriggerAll() {
	for _, ch := range r.triggers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (r *Runner) waitFirstPoll(ctx context.Context, max time.Duration) {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		all := r.deps.Health.All()
		complete := true
		for _, c := range r.deps.Config.Communities {
			snap, ok := all[c.ID]
			if !ok {
				complete = false
				break
			}
			// "Recorded once" = either a directory or an error.
			if snap.CurrentDirectory == nil && snap.LastError == "" {
				complete = false
				break
			}
		}
		if complete {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

// runCommunity is the long-lived polling goroutine for one community.
func (r *Runner) runCommunity(ctx context.Context, c config.Community, trig <-chan struct{}) {
	// First poll on startup.
	r.pollOnce(ctx, c)

	// Pure poll loop afterwards. After each successful new directory we
	// signal the global regenerate path.
	ticker := time.NewTicker(r.deps.Config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.pollOnce(ctx, c)
		case <-trig:
			r.pollOnce(ctx, c)
		}
	}
}

func (r *Runner) pollOnce(ctx context.Context, c config.Community) {
	// Use prior ETag if we have one.
	prev, _ := r.deps.Health.Get(c.ID)
	prevETag := prev.ETag

	res, err := r.deps.Fetcher.Fetch(ctx, c.ID, c.DirectoryURL, prevETag)
	if err != nil {
		r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
			s.LastError = err.Error()
			return s
		})
		r.logger.Printf("poller[%s]: fetch failed: %v", c.ID, err)
		return
	}

	if res.NotModified {
		r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
			s.LastSuccessfulPoll = time.Now()
			s.LastError = ""
			s.ETag = res.ETag
			return s
		})
		// No content change; nothing to regenerate.
		return
	}

	r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
		s.CurrentDirectory = res.Directory
		s.LastSuccessfulPoll = time.Now()
		s.LastError = ""
		s.ETag = res.ETag
		return s
	})

	if err := r.regenerate(ctx); err != nil {
		r.logger.Printf("poller[%s]: regenerate after successful poll: %v", c.ID, err)
	}
}

// regenerate is the serialized hot path: read every current directory
// from Health, hand them to Build, hash, and POST to Caddy only on
// change. Errors do not poison r.lastHash (so a future successful build
// is correctly detected as "changed" vs the last *applied* config).
func (r *Runner) regenerate(ctx context.Context) error {
	r.regenMu.Lock()
	defer r.regenMu.Unlock()

	dirs := make(map[string]*directory.Directory, len(r.deps.Config.Communities))
	for _, c := range r.deps.Config.Communities {
		snap, ok := r.deps.Health.Get(c.ID)
		if !ok || snap.CurrentDirectory == nil {
			continue
		}
		dirs[c.ID] = snap.CurrentDirectory
	}

	jsonBytes, err := r.build(caddyconfig.Input{
		Config:      r.deps.Config,
		Directories: dirs,
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	hash := sha256.Sum256(jsonBytes)
	if r.haveHash && hash == r.lastHash {
		return nil
	}

	if err := r.apply.Apply(ctx, jsonBytes); err != nil {
		// Do NOT update lastHash on failure so next poll retries.
		return fmt.Errorf("apply: %w", err)
	}
	r.lastHash = hash
	r.haveHash = true

	// Update the status server's prefix map.
	if r.deps.Sink != nil {
		prefixMap := make(map[string]string, len(dirs))
		for id, dir := range dirs {
			prefixMap[dir.Community.Prefix] = id
		}
		r.deps.Sink.SetPrefixMap(prefixMap)
	}
	return nil
}
