// Package poller — tsnet-backed Fetcher implementation.
//
// One ephemeral tsnet.Server is brought up per community at construction
// time; its HTTPClient is reused for every poll. Close stops every
// server.
package poller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bridge/internal/config"
	"bridge/internal/directory"

	"tailscale.com/tsnet"
)

// NewTsnetFetcher brings up one ephemeral tsnet node per community.
// The returned Fetcher is ready for concurrent Fetch calls.
//
// A community whose node fails to come up within
// cfg.CommunityJoinTimeout is recorded with a non-nil readyErr; its
// Fetch calls will keep returning that error until the bridge restarts.
func NewTsnetFetcher(ctx context.Context, cfg *config.Config) (*TsnetFetcher, error) {
	if cfg == nil {
		return nil, errors.New("poller: nil config")
	}
	tf := &TsnetFetcher{
		clients:  make(map[string]*http.Client, len(cfg.Communities)),
		servers:  make(map[string]*tsnet.Server, len(cfg.Communities)),
		readyErr: make(map[string]error, len(cfg.Communities)),
	}

	// Start nodes in parallel; each has its own deadline.
	var wg sync.WaitGroup
	for _, c := range cfg.Communities {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := tf.startOne(ctx, cfg, c)
			tf.mu.Lock()
			tf.readyErr[c.ID] = err
			tf.mu.Unlock()
		}()
	}
	wg.Wait()

	return tf, nil
}

// TsnetFetcher is the production Fetcher.
type TsnetFetcher struct {
	mu       sync.RWMutex
	clients  map[string]*http.Client
	servers  map[string]*tsnet.Server
	readyErr map[string]error
}

func (f *TsnetFetcher) startOne(ctx context.Context, cfg *config.Config, c config.Community) error {
	dir := filepath.Join(cfg.StateDir, "poller-"+c.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	srv := &tsnet.Server{
		AuthKey:   c.AuthKey,
		Hostname:  cfg.Personal.BridgeHostname + "-poller-" + c.ID,
		Dir:       dir,
		Ephemeral: true,
		Logf:      func(format string, args ...any) {}, // suppress chatter
	}
	upCtx, cancel := context.WithTimeout(ctx, cfg.CommunityJoinTimeout)
	defer cancel()
	if _, err := srv.Up(upCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("tsnet up: %w", err)
	}

	hc := srv.HTTPClient()
	hc.Timeout = 30 * time.Second

	f.mu.Lock()
	f.servers[c.ID] = srv
	f.clients[c.ID] = hc
	f.mu.Unlock()
	return nil
}

// Fetch implements Fetcher.
func (f *TsnetFetcher) Fetch(ctx context.Context, communityID, url, prevETag string) (*directory.FetchResult, error) {
	f.mu.RLock()
	hc, hasClient := f.clients[communityID]
	rerr := f.readyErr[communityID]
	f.mu.RUnlock()
	if !hasClient {
		if rerr != nil {
			return nil, fmt.Errorf("tsnet node not ready: %w", rerr)
		}
		return nil, fmt.Errorf("tsnet node not ready: unknown community %q", communityID)
	}
	return directory.Fetch(ctx, hc, url, prevETag)
}

// Ready implements Fetcher.
func (f *TsnetFetcher) Ready(communityID string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if _, ok := f.clients[communityID]; ok {
		return true, nil
	}
	return false, f.readyErr[communityID]
}

// Close stops every tsnet server.
func (f *TsnetFetcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var firstErr error
	for id, srv := range f.servers {
		if err := srv.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %s: %w", id, err)
		}
	}
	f.servers = nil
	f.clients = nil
	return firstErr
}
