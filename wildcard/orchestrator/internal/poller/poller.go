// Package poller drives the bridge's steady-state event loop (SPEC §10.3).
//
//   - Per-community goroutines re-fetch directories every poll_interval.
//   - One cert.Watcher goroutine reloads certs on file changes.
//   - A single regenerator mutex serializes
//     (read directories+certs) → (caddyconfig.Build) → (admin /load).
//   - SIGHUP fans out to every per-community goroutine AND triggers an
//     immediate cert re-check.
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
	"bridge/internal/cert"
	"bridge/internal/config"
	"bridge/internal/directory"
	"bridge/internal/health"
)

// Fetcher abstracts directory I/O. The production implementation lives
// in tsnetfetcher.go; tests substitute a fake.
type Fetcher interface {
	Fetch(ctx context.Context, communityID, url, prevETag, expectedDomain string) (*directory.FetchResult, error)
	Ready(communityID string) (bool, error)
	Close() error
}

// Applier is the side-effect target for a regen: it receives the
// freshly-built Caddy JSON. Production wiring sends to adminclient.Client.
type Applier interface {
	Apply(ctx context.Context, jsonConfig []byte) error
}

// adminApplier adapts *adminclient.Client to Applier.
type adminApplier struct{ c *adminclient.Client }

func (a adminApplier) Apply(ctx context.Context, j []byte) error { return a.c.Load(ctx, j) }

// CertLoader is the cert-reload entry point. Tests substitute a fake.
type CertLoader interface {
	Load(certPath, keyPath string) (*cert.Bundle, error)
}

type defaultCertLoader struct{}

func (defaultCertLoader) Load(c, k string) (*cert.Bundle, error) { return cert.Load(c, k) }

// Deps bundles the dependency injection points.
type Deps struct {
	Config  *config.Config
	Health  *health.Store
	Admin   *adminclient.Client
	Fetcher Fetcher

	// FallbackCertPath / FallbackKeyPath are passed through to
	// caddyconfig.Build (SPEC §12.1): when a community's wildcard cert
	// is missing or invalid, the bridge still binds its listener with
	// this self-signed pair so the user reaches the /__bridge_error
	// page (after clicking through the browser cert warning) instead
	// of an opaque network failure.
	FallbackCertPath string
	FallbackKeyPath  string

	// Optional overrides; nil means defaults.
	Build      func(caddyconfig.Input) ([]byte, error)
	CertLoader CertLoader
	CertVerify func(*cert.Bundle, string, time.Time) error
}

// Runner is the live polling loop. Construct via Start.
type Runner struct {
	deps   Deps
	apply  Applier
	build  func(caddyconfig.Input) ([]byte, error)
	loadc  CertLoader
	verify func(*cert.Bundle, string, time.Time) error

	// Cert bundles, keyed by community ID, protected by certsMu.
	certsMu sync.RWMutex
	certs   map[string]*cert.Bundle

	regenMu  sync.Mutex
	lastHash [32]byte
	haveHash bool

	triggers      map[string]chan struct{} // per-community fan-out
	certTriggerCh chan struct{}            // cert-watcher kick

	logger *log.Logger
}

// Start spawns one goroutine per community plus the cert watcher and
// returns a Runner whose TriggerAll method the signal handler can call
// on SIGHUP.
//
// Start blocks until each community's first poll has completed (or
// failed) and the initial cert bundles are loaded, so the first regen
// has data to work with.
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
	loadc := deps.CertLoader
	if loadc == nil {
		loadc = defaultCertLoader{}
	}
	verify := deps.CertVerify
	if verify == nil {
		verify = cert.Validate
	}
	r := &Runner{
		deps:          deps,
		apply:         adminApplier{c: deps.Admin},
		build:         build,
		loadc:         loadc,
		verify:        verify,
		certs:         make(map[string]*cert.Bundle, len(deps.Config.Communities)),
		triggers:      make(map[string]chan struct{}, len(deps.Config.Communities)),
		certTriggerCh: make(chan struct{}, 1),
		logger:        logger,
	}

	// Pre-load certs synchronously; record cert errors in Health so a
	// later /__bridge_status query can explain "this community is in
	// degraded mode". The fallback cert (Deps.FallbackCertPath) is what
	// keeps the listener bindable for cert-failed communities.
	r.reloadAllCerts()

	// Register community domains so dnscheck and status can attribute.
	for _, c := range deps.Config.Communities {
		deps.Health.SetDomain(c.ID, c.Domain)
	}

	// Spawn per-community poll goroutines.
	for _, c := range deps.Config.Communities {
		c := c
		trig := make(chan struct{}, 1)
		r.triggers[c.ID] = trig
		go r.runCommunity(ctx, c, trig)
	}

	// Spawn the cert-watch goroutine.
	go r.runCertWatcher(ctx)

	// Wait for first-poll watermark on every community (or timeout).
	r.waitFirstPoll(ctx, deps.Config.CommunityJoinTimeout+5*time.Second)

	// Initial regen even if some communities errored — Caddy still
	// gets a starting config covering the healthy ones.
	if err := r.regenerate(ctx); err != nil {
		logger.Printf("poller: initial regenerate: %v", err)
	}

	return r, nil
}

// TriggerAll wakes every per-community goroutine AND fires an immediate
// cert reload. Non-blocking; coalesces redundant signals.
func (r *Runner) TriggerAll() {
	for _, ch := range r.triggers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	select {
	case r.certTriggerCh <- struct{}{}:
	default:
	}
}

func (r *Runner) waitFirstPoll(ctx context.Context, max time.Duration) {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		all := r.deps.Health.All()
		complete := true
		for _, c := range r.deps.Config.Communities {
			snap, ok := all[c.ID]
			if !ok {
				complete = false
				break
			}
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
		case <-tick.C:
		}
	}
}

// runCommunity is the long-lived polling goroutine for one community.
func (r *Runner) runCommunity(ctx context.Context, c config.Community, trig <-chan struct{}) {
	// First poll on startup.
	r.pollOnce(ctx, c)

	tick := time.NewTicker(r.deps.Config.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.pollOnce(ctx, c)
		case <-trig:
			r.pollOnce(ctx, c)
		}
	}
}

func (r *Runner) pollOnce(ctx context.Context, c config.Community) {
	prev, _ := r.deps.Health.Get(c.ID)
	res, err := r.deps.Fetcher.Fetch(ctx, c.ID, c.DirectoryURL, prev.ETag, c.Domain)
	if err != nil {
		r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
			s.LastError = err.Error()
			s.LastPollAttempt = time.Now()
			return s
		})
		r.logger.Printf("poller[%s]: fetch failed: %v", c.ID, err)
		return
	}

	if res.NotModified {
		// 304 implicitly clears LastError: the directory we already
		// have is still the one the upstream is serving, so any prior
		// fetch error is no longer current. Callers reading the
		// snapshot get a single source of truth (current vs. stale).
		r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
			s.LastSuccessfulPoll = time.Now()
			s.LastPollAttempt = time.Now()
			s.LastError = ""
			s.ETag = res.ETag
			return s
		})
		return
	}

	r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
		s.CurrentDirectory = res.Directory
		s.LastSuccessfulPoll = time.Now()
		s.LastPollAttempt = time.Now()
		s.LastError = ""
		s.ETag = res.ETag
		return s
	})

	if err := r.regenerate(ctx); err != nil {
		r.logger.Printf("poller[%s]: regenerate after poll: %v", c.ID, err)
	}
}

// runCertWatcher polls every cert_check_interval AND on r.certTriggerCh.
func (r *Runner) runCertWatcher(ctx context.Context) {
	tick := time.NewTicker(r.deps.Config.CertCheckInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-r.certTriggerCh:
		}
		if r.checkCerts(ctx) {
			if err := r.regenerate(ctx); err != nil {
				r.logger.Printf("poller: regenerate after cert reload: %v", err)
			}
		}
	}
}

// reloadAllCerts loads every (cert,key) pair synchronously. Failures
// are recorded in Health but don't abort startup; the caller still
// proceeds to regenerate, because caddyconfig.Build now keeps the
// listener up using the fallback cert for cert-failed communities.
func (r *Runner) reloadAllCerts() {
	for _, c := range r.deps.Config.Communities {
		b, err := r.loadc.Load(c.CertPath, c.KeyPath)
		if err != nil {
			r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
				s.CertError = err.Error()
				return s
			})
			r.logger.Printf("poller[%s]: cert load: %v", c.ID, err)
			continue
		}
		now := time.Now()
		if vErr := r.verify(b, c.Domain, now); vErr != nil {
			r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
				s.CertError = vErr.Error()
				s.CertNotBefore = b.NotBefore
				s.CertNotAfter = b.NotAfter
				return s
			})
			r.logger.Printf("poller[%s]: cert validate: %v", c.ID, vErr)
			continue
		}
		r.certsMu.Lock()
		r.certs[c.ID] = b
		r.certsMu.Unlock()
		r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
			s.CertError = ""
			s.CertNotBefore = b.NotBefore
			s.CertNotAfter = b.NotAfter
			s.CertLastReload = now
			return s
		})
		r.warnExpiringSoon(c.ID, b, now)
	}
}

// warnExpiringSoon logs once when a cert has less than cert.MinValidity
// remaining (SPEC §9.4 step 3). Logs every check round; the dedupe is
// the operator's tail filter.
func (r *Runner) warnExpiringSoon(id string, b *cert.Bundle, now time.Time) {
	left := b.NotAfter.Sub(now)
	if left <= 0 || left >= cert.MinValidity {
		return
	}
	r.logger.Printf("poller[%s]: WARNING wildcard cert expires in %s (not_after=%s); ask the community admin to rotate (SPEC §6.3)",
		id, left.Truncate(time.Minute), b.NotAfter.Format(time.RFC3339))
}

// checkCerts re-loads every cert; returns true if anything changed.
func (r *Runner) checkCerts(ctx context.Context) (changed bool) {
	for _, c := range r.deps.Config.Communities {
		select {
		case <-ctx.Done():
			return changed
		default:
		}
		b, err := r.loadc.Load(c.CertPath, c.KeyPath)
		if err != nil {
			r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
				s.CertError = err.Error()
				return s
			})
			// Drop a previously-good cert if the file is now unreadable.
			r.certsMu.Lock()
			prev := r.certs[c.ID]
			delete(r.certs, c.ID)
			r.certsMu.Unlock()
			if prev != nil {
				changed = true
			}
			continue
		}
		now := time.Now()
		if vErr := r.verify(b, c.Domain, now); vErr != nil {
			r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
				s.CertError = vErr.Error()
				s.CertNotBefore = b.NotBefore
				s.CertNotAfter = b.NotAfter
				return s
			})
			r.certsMu.Lock()
			prev := r.certs[c.ID]
			delete(r.certs, c.ID)
			r.certsMu.Unlock()
			if prev != nil {
				changed = true
			}
			continue
		}
		r.certsMu.Lock()
		prev := r.certs[c.ID]
		r.certs[c.ID] = b
		r.certsMu.Unlock()
		if prev == nil || prev.ContentHash != b.ContentHash {
			r.deps.Health.Update(c.ID, func(s health.Snapshot) health.Snapshot {
				s.CertError = ""
				s.CertNotBefore = b.NotBefore
				s.CertNotAfter = b.NotAfter
				s.CertLastReload = now
				return s
			})
			changed = true
		}
		r.warnExpiringSoon(c.ID, b, now)
	}
	return changed
}

// regenerate is the serialized hot path: read every current directory
// and cert bundle, hand them to Build, hash, and POST to Caddy only on
// change. Errors do NOT poison r.lastHash.
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

	r.certsMu.RLock()
	certs := make(map[string]*cert.Bundle, len(r.certs))
	for k, v := range r.certs {
		certs[k] = v
	}
	r.certsMu.RUnlock()

	jsonBytes, err := r.build(caddyconfig.Input{
		Config:           r.deps.Config,
		Directories:      dirs,
		Certs:            certs,
		FallbackCertPath: r.deps.FallbackCertPath,
		FallbackKeyPath:  r.deps.FallbackKeyPath,
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	hash := sha256.Sum256(jsonBytes)
	if r.haveHash && hash == r.lastHash {
		return nil
	}

	if err := r.apply.Apply(ctx, jsonBytes); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	r.lastHash = hash
	r.haveHash = true
	return nil
}
