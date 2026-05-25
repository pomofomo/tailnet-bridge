// Package caddyconfig builds the Caddy JSON config from the bridge's
// merged inputs: parsed config.yml, the latest directory per community,
// and the latest validated cert bundle per community. The build is a
// pure function — no I/O, no globals — so it can be table-tested.
//
// See SPEC §9 for the structure produced; CLAUDE.md "Caddy Generation
// Notes" summarizes the per-community site-block shape.
//
// Status: STUB.
package caddyconfig

import (
	"errors"

	"bridge/internal/cert"
	"bridge/internal/config"
	"bridge/internal/directory"
)

// CommunityID is a typed alias so map keys can't be mixed up with raw
// strings elsewhere.
type CommunityID = string

// Inputs is the full set of values Build needs. Passing a struct keeps
// the signature stable as fields are added.
type Inputs struct {
	Personal    config.Personal
	Communities []config.Community
	Directories map[CommunityID]*directory.Directory
	Certs       map[CommunityID]*cert.Bundle

	// Loopback port the orchestrator's error/status server listens on;
	// every site block reverse-proxies to this for handle_errors and the
	// catch-all unknown-subdomain handler.
	ErrorPort int
}

// Build emits a deterministic Caddy JSON document covering every
// community present in BOTH Directories and Certs. Communities present
// in Inputs.Communities but missing a directory or a valid cert are
// silently omitted from the listener config; the status server still
// reports them.
//
// Per-community shape (SPEC §9.2):
//
//	*.<community.domain> {
//	    bind tailscale/personal-<id>
//	    tls <cert_path> <key_path>
//	    @<svc> host <svc>.<community.domain>
//	    handle @<svc> {
//	        reverse_proxy https://<upstream_tailnet_host>:<port> {
//	            transport tailscale community-dialer-<id> { tls }
//	            header_up X-Tailscale-User       {http.auth.user.tailscale_login}
//	            header_up X-Tailscale-User-Email {http.auth.user.tailscale_user}
//	            header_up X-Tailscale-User-Name  {http.auth.user.tailscale_name}
//	            header_up X-Tailscale-Node       {http.auth.user.tailscale_node}
//	        }
//	    }
//	    handle { rewrite * /__bridge_error
//	             reverse_proxy http://127.0.0.1:<ErrorPort> }
//	    handle_errors { rewrite * /__bridge_error
//	                    reverse_proxy http://127.0.0.1:<ErrorPort> }
//	}
//
// The `Host` header is intentionally NOT rewritten (SPEC §3.2, §5.1):
// same canonical name on both sides of the bridge.
//
// Tailscale app block (SPEC §9.1):
//   - One personal-side listener node per community: `personal-<id>`,
//     hostname "<id>-bridge", joining the personal tailnet.
//   - One community-side dialer node per community:
//     `community-dialer-<id>`, hostname "<personal.bridge_hostname>",
//     joining the community tailnet.
func Build(in Inputs) ([]byte, error) {
	// TODO(impl):
	//   1. Construct the `apps.tailscale.nodes` map from Inputs.Communities,
	//      using config.ResolvedAuthKey for each tsnet node.
	//   2. Construct `apps.http.servers["srv0"].routes` with one route
	//      per community (matcher: hosts under *.<community.domain>),
	//      each holding the per-service `handle`s and the catch-all +
	//      handle_errors.
	//   3. json.MarshalIndent for stability.
	//   4. The output MUST be byte-identical for identical Inputs so
	//      the orchestrator can compare hashes to decide whether to POST.
	_ = in
	return nil, errNotImplemented
}

// PersonalNodeName returns the tsnet node id used for the listener that
// terminates *.<community.domain> on the personal tailnet.
func PersonalNodeName(id CommunityID) string {
	return "personal-" + id
}

// DialerNodeName returns the tsnet node id used to dial upstreams on
// the community tailnet for the given community.
func DialerNodeName(id CommunityID) string {
	return "community-dialer-" + id
}

var errNotImplemented = errors.New("caddyconfig: not yet implemented")
