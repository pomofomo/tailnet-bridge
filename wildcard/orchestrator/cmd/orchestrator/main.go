// Command orchestrator is the bridge's PID-1 process. It owns the
// lifecycle described in SPEC §10.3: parse config, validate wildcard
// cert files, spawn Caddy, poll each community directory over an
// ephemeral tsnet node, push merged Caddy JSON to the admin API, watch
// cert files for rotation, run the embedded DNS responder, run the
// error/status server, propagate signals.
//
// Status: STUB. Wires every internal package together with the contracts
// declared in CLAUDE.md but does not yet drive them. See the TODOs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"bridge/internal/config"
)

const defaultConfigPath = "/etc/bridge/config.yml"

func main() {
	logger := log.New(os.Stderr, "orchestrator: ", log.LstdFlags|log.Lmsgprefix)
	if err := run(logger); err != nil {
		logger.Fatalf("fatal: %v", err)
	}
}

func run(logger *log.Logger) error {
	configPath := flag.String("config", envOr("BRIDGE_CONFIG", defaultConfigPath),
		"path to config.yml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	_ = cfg

	// TODO(impl): full lifecycle, per SPEC §10.3:
	//
	//   1. Pre-flight: validate every (cert_path, key_path) pair via
	//      cert.Load + cert.Validate(expectedDomain=community.domain).
	//      Communities that fail validation are skipped (logged) but the
	//      bridge keeps running for the rest. This is checked again on
	//      every cert-watcher tick.
	//
	//   2. Spawn Caddy via internal/caddyproc with caddy/bootstrap.json,
	//      then wait for 127.0.0.1:2019 to accept connections.
	//
	//   3. For each community:
	//        - Bring up an ephemeral tsnet.Server joined to the community
	//          tailnet.
	//        - directory.Fetch the directory; store on success.
	//        - Tear down the ephemeral node.
	//
	//   4. Build the initial Caddy JSON via caddyconfig.Build(cfg, dirs,
	//      certs) and POST to admin via adminclient.Load.
	//
	//   5. Start background goroutines:
	//        - internal/poller (per-community directory polls; SIGHUP
	//          fan-out; config-regen mutex).
	//        - internal/cert.Watcher on every (cert_path, key_path) pair.
	//        - internal/dnscheck (every poll_interval, resolve community
	//          domains over the host's public resolver and warn on any
	//          positive answer; SPEC §3.5, §9.5).
	//        - internal/dns.Server on UDP/53 of each personal-side
	//          listener node (SPEC §7.3, §10.1). Answers
	//          *.<community.domain> with that node's tailnet IP.
	//        - internal/status.Server on 127.0.0.1:<orchestrator_error_port>
	//          for /__bridge_error and /__bridge_status.
	//
	//   6. On SIGHUP: poller does an immediate re-poll of every
	//      community AND a fresh cert-file stat round.
	//
	//   7. On SIGTERM/SIGINT: signal the cancellable root context,
	//      caddyproc.Shutdown with 30s grace, exit 0.

	logger.Printf("orchestrator stub: config parsed; lifecycle not yet implemented")
	return errors.New("not yet implemented; see TODOs in cmd/orchestrator/main.go")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
