// Command orchestrator is the bridge's PID-1 process for the wildcard
// variant. It owns the lifecycle described in SPEC §10.3:
//
//  1. Parse + validate config.
//  2. Spawn Caddy with bootstrap.json (admin API only).
//  3. Wait for Caddy admin to accept connections.
//  4. Start the status/error server on 127.0.0.1:<orchestrator_error_port>.
//  5. Bring up one ephemeral tsnet poller node per community, fetch
//     each directory, validate against the local config's domain.
//  6. Load + validate every (cert,key) pair. Build the initial Caddy
//     JSON, POST to admin /load.
//  7. Start the poller (per-community polling, cert-watch ticker,
//     SIGHUP fan-out, regen+apply with hash dedupe).
//  8. Start the dnscheck goroutine: warn if any community domain
//     resolves on the public internet (SPEC §3.5, §9.5).
//  9. Wait for SIGTERM / SIGINT / unexpected Caddy exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"bridge/internal/adminclient"
	"bridge/internal/caddyproc"
	"bridge/internal/config"
	"bridge/internal/dnscheck"
	"bridge/internal/health"
	"bridge/internal/poller"
	"bridge/internal/status"
)

const shutdownGrace = 30 * time.Second

func main() {
	logger := log.New(os.Stderr, "orchestrator: ", log.LstdFlags|log.Lmsgprefix)
	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	cfgPath := os.Getenv("BRIDGE_CONFIG")
	if cfgPath == "" {
		cfgPath = "/etc/bridge/config.yml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	logger.Printf("config loaded: %d communities, base=%s, poll=%s, cert_check=%s",
		len(cfg.Communities), cfg.BaseDomain(), cfg.PollInterval, cfg.CertCheckInterval)

	// Spawn Caddy.
	bootstrap := os.Getenv("CADDY_BOOTSTRAP")
	if bootstrap == "" {
		bootstrap = "/etc/bridge/caddy-bootstrap.json"
	}
	caddyBin := os.Getenv("CADDY_BIN")
	if caddyBin == "" {
		caddyBin = "caddy"
	}

	procCtx, cancelProc := context.WithCancel(context.Background())
	defer cancelProc()
	proc, err := caddyproc.Start(procCtx, caddyBin, bootstrap)
	if err != nil {
		return fmt.Errorf("spawn caddy: %w", err)
	}

	if err := waitForAdmin(cfg.CaddyAdminAddr, 10*time.Second); err != nil {
		_ = proc.Terminate()
		_ = proc.Wait()
		return fmt.Errorf("caddy admin never came up at %s: %w", cfg.CaddyAdminAddr, err)
	}
	logger.Printf("caddy admin ready at %s", cfg.CaddyAdminAddr)

	healthStore := health.NewStore()

	// Status server.
	statusSrv := &status.Server{
		Addr:   "127.0.0.1:" + strconv.Itoa(cfg.OrchestratorErrorPort),
		Health: healthStore,
	}
	statusCtx, cancelStatus := context.WithCancel(context.Background())
	defer cancelStatus()
	statusErrCh := make(chan error, 1)
	go func() { statusErrCh <- statusSrv.Run(statusCtx) }()
	logger.Printf("status server listening on %s", statusSrv.Addr)

	// tsnet poller fetchers (one ephemeral node per community).
	pollerCtx, cancelPoller := context.WithCancel(context.Background())
	defer cancelPoller()
	fetcher, err := poller.NewTsnetFetcher(pollerCtx, cfg)
	if err != nil {
		_ = proc.Terminate()
		_ = proc.Wait()
		return fmt.Errorf("tsnet fetcher: %w", err)
	}
	defer fetcher.Close()

	admin := &adminclient.Client{Addr: cfg.CaddyAdminAddr}

	runner, err := poller.Start(pollerCtx, poller.Deps{
		Config:  cfg,
		Health:  healthStore,
		Admin:   admin,
		Fetcher: fetcher,
	}, logger)
	if err != nil {
		_ = proc.Terminate()
		_ = proc.Wait()
		return fmt.Errorf("poller: %w", err)
	}

	// DNS sanity check goroutine (SPEC §9.5).
	dnsCtx, cancelDNS := context.WithCancel(context.Background())
	defer cancelDNS()
	domains := make([]string, len(cfg.Communities))
	for i, c := range cfg.Communities {
		domains[i] = c.Domain
	}
	checker := &dnscheck.Checker{
		Domains:  domains,
		Resolver: cfg.DNSCheckResolver,
		Interval: cfg.PollInterval,
		OnResult: func(r dnscheck.Result) {
			healthStore.RecordDNSResult(r)
			if r.Violation {
				logger.Printf("dnscheck: PUBLIC DNS LEAK: %s resolves to %v (resolver=%s) — SPEC §3.5 invariant violated",
					r.Domain, r.Answers, r.Resolver)
			}
		},
	}
	go func() {
		if err := checker.Run(dnsCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("dnscheck: %v", err)
		}
	}()

	// Signal handling.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	caddyExit := make(chan error, 1)
	go func() { caddyExit <- proc.Wait() }()

	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		shutdownOnce.Do(func() {
			logger.Printf("shutdown initiated: %s", reason)
			cancelDNS()
			cancelPoller()
			cancelStatus()
			_ = proc.Terminate()
			select {
			case <-caddyExit:
			case <-time.After(shutdownGrace):
				logger.Printf("caddy did not exit within %s; killing", shutdownGrace)
				_ = proc.Signal(syscall.SIGKILL)
				<-caddyExit
			}
		})
	}

	for {
		select {
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				logger.Printf("SIGHUP: triggering re-poll + cert recheck")
				runner.TriggerAll()
			case syscall.SIGTERM, syscall.SIGINT:
				shutdown(sig.String())
				return nil
			}
		case err := <-caddyExit:
			caddyExit <- err
			if err == nil {
				logger.Printf("caddy exited cleanly; orchestrator exiting")
				cancelDNS()
				cancelPoller()
				cancelStatus()
				return nil
			}
			shutdown("caddy exited unexpectedly")
			return fmt.Errorf("caddy exited: %w", err)
		case err := <-statusErrCh:
			if err != nil {
				logger.Printf("status server failed: %v", err)
				shutdown("status server failed")
				return err
			}
		}
	}
}

// waitForAdmin polls the Caddy admin TCP socket until it accepts a
// connection or the timeout elapses.
func waitForAdmin(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}
