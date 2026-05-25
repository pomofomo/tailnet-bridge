// Package caddyconfig builds the Caddy JSON config (SPEC §9) from the
// bridge config, the latest directory per community, and the validated
// cert paths.
//
// Build is a pure function: given identical inputs, it returns
// byte-identical output. The poller compares output hashes to decide
// whether a /load POST is needed.
//
// The wildcard-domain layout differs structurally from the caddy variant:
//
//   - ONE personal-side listener tsnet node per community (SPEC §5.1).
//   - ONE site per community, matching `*.<community.domain>`, dispatching
//     services by Host-header matchers (SPEC §9.2).
//   - The community's wildcard cert/key are loaded as static files via
//     `apps.tls.certificates.load_files`; no automatic_https.
//   - The bridgedns app receives one node per community so the user's
//     personal-tailnet Split DNS works (SPEC §10.1).
//   - Identity headers are preserved upstream; Host header is preserved
//     (NOT rewritten), since the canonical name is the same on both
//     sides of the bridge (SPEC §3.2).
//   - The transport's `tls.server_name` is set to the CANONICAL name
//     (e.g. wiki.smithfamily.ts.example.com), not the dial target
//     (wiki.smithfamily.ts.net), so the upstream's wildcard cert validates.
//
// Cert-failed communities (SPEC §12.1): when a community's wildcard cert
// is missing or invalid, we still emit its personal-side listener and
// bridgedns entry, but bind a self-signed fallback cert (from
// internal/fallbackcert) and route every host through the bridge error
// page. The browser will show a cert warning, but after the user clicks
// through they reach a friendly explanation instead of an opaque network
// error.
package caddyconfig

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"

	"bridge/internal/cert"
	"bridge/internal/config"
	"bridge/internal/directory"
)

// Input bundles everything Build needs. Map keys are config-level
// community IDs.
type Input struct {
	Config      *config.Config
	Directories map[string]*directory.Directory
	Certs       map[string]*cert.Bundle

	// FallbackCertPath / FallbackKeyPath are used when a community's
	// real cert is missing or invalid (SPEC §12.1). Both must be set
	// or both empty; if empty, cert-failed communities are skipped
	// from the output entirely (the old behaviour).
	FallbackCertPath string
	FallbackKeyPath  string
}

// PersonalNodeName returns the tsnet node id used for the personal-side
// listener that terminates *.<community.domain> on the personal tailnet.
func PersonalNodeName(id string) string { return "personal-" + id }

// DialerNodeName returns the tsnet node id used to dial upstreams on
// the community tailnet for the given community.
func DialerNodeName(id string) string { return "community-dialer-" + id }

const fallbackTag = "fallback"

func certTag(id string) string { return "cert-" + id }

// Build emits the Caddy JSON for the given inputs.
//
// Every community in cfg.Communities is reachable in the output, even
// when its cert or directory is missing — we still bind the personal
// listener and DNS responder so the user gets the /__bridge_error page
// instead of an opaque network failure. A community is dropped only if
// it has neither a real cert nor a fallback configured.
func Build(in Input) ([]byte, error) {
	if in.Config == nil {
		return nil, errors.New("caddyconfig: nil config")
	}
	if (in.FallbackCertPath == "") != (in.FallbackKeyPath == "") {
		return nil, errors.New("caddyconfig: FallbackCertPath and FallbackKeyPath must both be set or both empty")
	}
	cfg := in.Config
	hasFallback := in.FallbackCertPath != ""

	// Determine which communities make it into the output, and which
	// are "healthy" (real cert + directory with services → reverse
	// proxy) versus "degraded" (error-page only).
	type state struct {
		c         config.Community
		hasCert   bool
		hasDir    bool
		serviceOK bool // hasCert && hasDir && len(services) > 0
	}
	included := make([]state, 0, len(cfg.Communities))
	for _, c := range cfg.Communities {
		st := state{c: c}
		_, st.hasCert = in.Certs[c.ID]
		dir, hasDir := in.Directories[c.ID]
		st.hasDir = hasDir
		st.serviceOK = st.hasCert && st.hasDir && dir != nil && len(dir.Services) > 0
		if !st.hasCert && !hasFallback {
			// No real cert and no fallback to bind a listener with —
			// nothing useful we can emit for this community.
			continue
		}
		included = append(included, st)
	}
	sort.Slice(included, func(i, j int) bool { return included[i].c.ID < included[j].c.ID })

	// tsnet nodes. Personal listener always exists for every included
	// community. Community-side dialer is only useful if we'll emit
	// service routes — skip it otherwise (H2).
	tsNodes := make(map[string]any, 2*len(included))
	for _, st := range included {
		tsNodes[PersonalNodeName(st.c.ID)] = map[string]any{
			"auth_key":  cfg.Personal.AuthKey,
			"hostname":  st.c.ID + "-bridge",
			"state_dir": filepath.Join(cfg.StateDir, PersonalNodeName(st.c.ID)),
			"ephemeral": false,
		}
		if st.serviceOK {
			tsNodes[DialerNodeName(st.c.ID)] = map[string]any{
				"auth_key":  st.c.AuthKey,
				"hostname":  cfg.Personal.BridgeHostname,
				"state_dir": filepath.Join(cfg.StateDir, DialerNodeName(st.c.ID)),
				"ephemeral": false,
			}
		}
	}

	// load_files: one entry per community with a real cert, plus a
	// single fallback entry if any community needs it.
	loadFiles := make([]map[string]any, 0, len(included)+1)
	fallbackNeeded := false
	for _, st := range included {
		if st.hasCert {
			loadFiles = append(loadFiles, map[string]any{
				"certificate": st.c.CertPath,
				"key":         st.c.KeyPath,
				"tags":        []string{certTag(st.c.ID)},
			})
		} else {
			fallbackNeeded = true
		}
	}
	if fallbackNeeded {
		loadFiles = append(loadFiles, map[string]any{
			"certificate": in.FallbackCertPath,
			"key":         in.FallbackKeyPath,
			"tags":        []string{fallbackTag},
		})
	}

	// http servers: one per included community.
	servers := make(map[string]any, len(included))
	for _, st := range included {
		servers[st.c.ID] = buildServer(cfg, st.c, in.Directories[st.c.ID], st.hasCert, st.serviceOK)
	}

	// bridgedns nodes: one per included community.
	bridgednsNodes := make(map[string]any, len(included))
	for _, st := range included {
		bridgednsNodes[st.c.ID] = map[string]any{
			"tsnet_node": PersonalNodeName(st.c.ID),
			"domain":     st.c.Domain,
			"port":       53,
		}
	}

	out := map[string]any{
		"admin": map[string]any{
			"listen": cfg.CaddyAdminAddr,
		},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{"level": "INFO"},
			},
		},
		"apps": map[string]any{
			"tailscale": map[string]any{
				"nodes": tsNodes,
			},
			"tls": map[string]any{
				"certificates": map[string]any{
					"load_files": loadFiles,
				},
			},
			"http": map[string]any{
				"servers": servers,
			},
			"bridgedns": map[string]any{
				"nodes": bridgednsNodes,
			},
		},
	}
	return json.MarshalIndent(out, "", "  ")
}

// buildServer emits one apps.http.servers[<id>] entry.
//
// When serviceOK is true we emit one route per directory service plus a
// catch-all unknown-subdomain handler. When false (no cert, no dir, no
// services), every host under the wildcard routes to the error page so
// the user gets a coherent explanation rather than a connection error.
func buildServer(cfg *config.Config, c config.Community, dir *directory.Directory, hasCert, serviceOK bool) map[string]any {
	wildcard := "*." + c.Domain

	routes := make([]map[string]any, 0, 4)
	if serviceOK {
		svcs := make([]directory.Service, len(dir.Services))
		copy(svcs, dir.Services)
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
		for _, svc := range svcs {
			routes = append(routes, buildServiceRoute(cfg, c, dir, svc))
		}
	}
	// Catch-all wildcard hit (typo, unknown service, degraded community)
	// → error page. Always last; matches anything under *.<domain> that
	// the per-service routes didn't terminate on.
	routes = append(routes, map[string]any{
		"match":    []map[string]any{{"host": []string{wildcard}}},
		"handle":   errorHandle(cfg),
		"terminal": true,
	})

	tag := certTag(c.ID)
	if !hasCert {
		tag = fallbackTag
	}

	return map[string]any{
		"listen":          []string{"tailscale/" + PersonalNodeName(c.ID) + ":443"},
		"automatic_https": map[string]any{"disable": true},
		"tls_connection_policies": []map[string]any{
			{"certificate_selection": map[string]any{"any_tag": []string{tag}}},
		},
		"routes": routes,
		"errors": map[string]any{
			"routes": []map[string]any{
				{"handle": errorHandle(cfg)},
			},
		},
	}
}

func buildServiceRoute(cfg *config.Config, c config.Community, dir *directory.Directory, svc directory.Service) map[string]any {
	canonical := dir.CanonicalHostname(svc)
	upstreamHostPort := svc.UpstreamTailnetHost + ":" + strconv.Itoa(svc.UpstreamPort)

	// Request headers: preserve Host (canonical name is the same on
	// both sides); propagate Tailscale identity placeholders.
	reqSet := map[string][]string{
		"Host":                   {"{http.request.host}"},
		"X-Forwarded-Host":       {"{http.request.host}"},
		"X-Forwarded-Proto":      {"https"},
		"X-Tailscale-User":       {"{http.auth.user.tailscale_login}"},
		"X-Tailscale-User-Email": {"{http.auth.user.tailscale_user}"},
		"X-Tailscale-User-Name":  {"{http.auth.user.tailscale_name}"},
		"X-Tailscale-Node":       {"{http.auth.user.tailscale_node}"},
		"X-Tailscale-Tailnet":    {"{http.auth.user.tailscale_tailnet}"},
	}

	reverseProxy := map[string]any{
		"handler": "reverse_proxy",
		"transport": map[string]any{
			"protocol": "tailscale",
			"name":     DialerNodeName(c.ID),
			// Override SNI / cert verification target: dial the tailnet
			// hostname, validate the wildcard cert under the canonical
			// name (SPEC §3.2).
			"tls": map[string]any{
				"server_name": canonical,
			},
		},
		"upstreams": []map[string]any{{"dial": upstreamHostPort}},
		"headers": map[string]any{
			"request": map[string]any{"set": reqSet},
		},
	}

	auth := map[string]any{
		"handler": "authentication",
		"providers": map[string]any{
			"tailscale": map[string]any{},
		},
	}

	return map[string]any{
		"match":    []map[string]any{{"host": []string{canonical}}},
		"handle":   []map[string]any{auth, reverseProxy},
		"terminal": true,
	}
}

// errorHandle returns the handler chain that rewrites to /__bridge_error
// and reverse-proxies to the orchestrator's status server.
func errorHandle(cfg *config.Config) []map[string]any {
	return []map[string]any{
		{"handler": "rewrite", "uri": "/__bridge_error"},
		{
			"handler": "reverse_proxy",
			"upstreams": []map[string]any{
				{"dial": "127.0.0.1:" + strconv.Itoa(cfg.OrchestratorErrorPort)},
			},
		},
	}
}

// ─── debug helpers ──────────────────────────────────────────────────────

// Hash returns sha256 of Build's output; used by the poller to dedupe
// regenerations. Re-exported for tests.
//
//goland:noinspection GoUnusedExportedFunction
func Hash(out []byte) [32]byte { return sha256.Sum256(out) }
