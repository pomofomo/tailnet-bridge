// Package caddyconfig builds the Caddy JSON config (SPEC §8) from the
// bridge config plus the merged set of community directories.
//
// Build is a pure function: given identical inputs it must produce
// byte-identical output. This invariant is what lets the poller compare
// hashes to decide whether a reload is needed.
package caddyconfig

import (
	"bridge/internal/config"
	"bridge/internal/directory"
)

// Input bundles everything Build needs. The map key is the community ID
// from config.yml (NOT the directory's community.name).
type Input struct {
	Config      *config.Config
	Directories map[string]*directory.Directory
	// PersonalTailnet is the DNS suffix used to form personal-side hostnames,
	// e.g. "alice.ts.net". Resolved by the orchestrator from the personal
	// tsnet node (or configured directly later).
	PersonalTailnet string
}

// Build returns the JSON payload to POST to Caddy's /load endpoint.
//
// Generation rules (SPEC §8):
//
//   apps.tailscale.nodes:
//     For each (community, service) pair:
//       personal-<community.id>-<service.name>:
//         hostname:  <community.prefix><service.name>
//         auth_key:  cfg.Personal.AuthKey
//         ephemeral: false
//         state_dir: <state_dir>/personal-<community.id>-<service.name>
//     For each community:
//       community-dialer-<community.id>:
//         hostname:  <cfg.Personal.BridgeHostname>
//         auth_key:  community.AuthKey
//         ephemeral: false
//         state_dir: <state_dir>/community-dialer-<community.id>
//
//   apps.http.servers: one server per (community, service) with:
//     - listen via bind tailscale/personal-<id>-<name>
//     - tls.get_certificate "tailscale"
//     - reverse_proxy upstream via transport tailscale community-dialer-<id> {tls}
//     - header_up: identity (X-Tailscale-*), Host {upstream_hostport},
//                  X-Forwarded-Host {host}, X-Forwarded-Proto https
//     - header_down: rewrite Location and Set-Cookie
//     - if RewriteBody: Accept-Encoding identity up, replace-response down
//       for https://<host>, //<host>, plus rewrite_extra_hosts entries,
//       then encode gzip
//     - handle_errors: rewrite /__bridge_error, reverse_proxy
//       http://127.0.0.1:<orchestrator_error_port>
//
// Collisions: if two (community, service) pairs produce the same
// personal-side hostname, Build returns an error and the caller refuses
// the reload (SPEC §10.7).
func Build(in Input) ([]byte, error) {
	return nil, nil
}
