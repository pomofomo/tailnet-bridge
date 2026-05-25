// Command orchestrator is the bridge's PID-1 process. It owns the
// lifecycle described in SPEC §9: parse config, spawn Caddy, poll each
// community directory over an ephemeral tsnet node, push merged Caddy
// JSON to the admin API, run the error/status server, propagate signals.
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
	"bridge/internal/health"
	"bridge/internal/poller"
	"bridge/internal/status"
)

const (
	// shutdownGrace is the maximum we wait for Caddy to exit after
	// forwarding SIGTERM/SIGINT (SPEC §9.1 step 6).
	shutdownGrace = 30 * time.Second
)

func main() {
	logger := log.New(os.Stderr, "orchestrator: ", log.LstdFlags|log.Lmsgprefix)

	if err := run(logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) error {
	// 1. Parse config.
	cfgPath := os.Getenv("BRIDGE_CONFIG")
	if cfgPath == "" {
		cfgPath = "/etc/bridge/config.yml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	logger.Printf("config loaded: %d communities, poll_interval=%s", len(cfg.Communities), cfg.PollInterval)

	// 2. Spawn Caddy.
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

	// Block until the admin endpoint is reachable so the first /load
	// call in poller.Start doesn't race against caddy initialization.
	if err := waitForAdmin(cfg.CaddyAdminAddr, 10*time.Second); err != nil {
		_ = proc.Terminate()
		_ = proc.Wait()
		return fmt.Errorf("caddy admin never came up at %s: %w", cfg.CaddyAdminAddr, err)
	}
	logger.Printf("caddy admin ready at %s", cfg.CaddyAdminAddr)

	// 3. Status server (its prefix map will be populated by the poller).
	healthStore := health.NewStore()
	statusSrv := &status.Server{
		Addr:   "127.0.0.1:" + strconv.Itoa(cfg.OrchestratorErrorPort),
		Health: healthStore,
	}
	statusCtx, cancelStatus := context.WithCancel(context.Background())
	defer cancelStatus()
	statusErrCh := make(chan error, 1)
	go func() { statusErrCh <- statusSrv.Run(statusCtx) }()
	logger.Printf("status server listening on %s", statusSrv.Addr)

	// 4. Fetcher (one tsnet node per community).
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
		Sink:    statusSrv,
	}, logger)
	if err != nil {
		_ = proc.Terminate()
		_ = proc.Wait()
		return fmt.Errorf("poller: %w", err)
	}

	// 5. Signal handling.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Channel surfacing Caddy's exit (unexpected → orchestrator exits 1).
	caddyExit := make(chan error, 1)
	go func() { caddyExit <- proc.Wait() }()

	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		shutdownOnce.Do(func() {
			logger.Printf("shutdown initiated: %s", reason)
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
				logger.Printf("SIGHUP: triggering re-poll")
				runner.TriggerAll()
			case syscall.SIGTERM, syscall.SIGINT:
				shutdown(sig.String())
				return nil
			}
		case err := <-caddyExit:
			caddyExit <- err // re-arm so shutdown() can drain
			if err == nil {
				logger.Printf("caddy exited cleanly; orchestrator exiting")
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
