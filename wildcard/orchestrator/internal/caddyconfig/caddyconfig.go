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
}

// PersonalNodeName returns the tsnet node id used for the personal-side
// listener that terminates *.<community.domain> on the personal tailnet.
func PersonalNodeName(id string) string { return "personal-" + id }

// DialerNodeName returns the tsnet node id used to dial upstreams on
// the community tailnet for the given community.
func DialerNodeName(id string) string { return "community-dialer-" + id }

// Build emits the Caddy JSON for the given inputs. A community is
// included in the listener config only if BOTH Directories and Certs
// have an entry for it; communities with missing/invalid certs are
// silently omitted (the status server still reports them).
func Build(in Input) ([]byte, error) {
	if in.Config == nil {
		return nil, errors.New("caddyconfig: nil config")
	}
	cfg := in.Config

	// Sort the eligible community IDs for deterministic output.
	ids := make([]string, 0, len(cfg.Communities))
	communities := make(map[string]config.Community, len(cfg.Communities))
	for _, c := range cfg.Communities {
		communities[c.ID] = c
		_, hasDir := in.Directories[c.ID]
		_, hasCert := in.Certs[c.ID]
		if !hasDir || !hasCert {
			continue
		}
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)

	// Build apps.tailscale.nodes (every active community contributes
	// one personal listener + one community dialer).
	tsNodes := make(map[string]any, 2*len(ids))
	for _, id := range ids {
		c := communities[id]
		tsNodes[PersonalNodeName(id)] = map[string]any{
			"auth_key":  cfg.Personal.AuthKey,
			"hostname":  id + "-bridge",
			"state_dir": filepath.Join(cfg.StateDir, PersonalNodeName(id)),
			"ephemeral": false,
		}
		tsNodes[DialerNodeName(id)] = map[string]any{
			"auth_key":  c.AuthKey,
			"hostname":  cfg.Personal.BridgeHostname,
			"state_dir": filepath.Join(cfg.StateDir, DialerNodeName(id)),
			"ephemeral": false,
		}
	}

	// Build apps.tls.certificates.load_files. One entry per active
	// community, paths read from config (not from cert.Bundle — the
	// Bundle is the validation result; Caddy re-reads the file).
	loadFiles := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		c := communities[id]
		loadFiles = append(loadFiles, map[string]any{
			"certificate": c.CertPath,
			"key":         c.KeyPath,
			"tags":        []string{id},
		})
	}

	// Build apps.http.servers: one server per community.
	servers := make(map[string]any, len(ids))
	for _, id := range ids {
		servers[id] = buildServer(cfg, communities[id], in.Directories[id])
	}

	// Build apps.bridgedns.nodes: one DNS responder bound to each
	// community's tsnet listener node (SPEC §10.1).
	bridgednsNodes := make(map[string]any, len(ids))
	for _, id := range ids {
		bridgednsNodes[id] = map[string]any{
			"tsnet_node": PersonalNodeName(id),
			"domain":     communities[id].Domain,
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

// buildServer emits one apps.http.servers[<id>] entry, one route per
// service in the directory plus the catch-all unknown-subdomain handler.
func buildServer(cfg *config.Config, c config.Community, dir *directory.Directory) map[string]any {
	wildcard := "*." + c.Domain

	// Sort services by name for deterministic routes ordering.
	svcs := make([]directory.Service, len(dir.Services))
	copy(svcs, dir.Services)
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })

	routes := make([]map[string]any, 0, len(svcs)+1)
	for _, svc := range svcs {
		routes = append(routes, buildServiceRoute(cfg, c, dir, svc))
	}
	// Catch-all unknown subdomain under the wildcard → error page.
	routes = append(routes, map[string]any{
		"match":    []map[string]any{{"host": []string{wildcard}}},
		"handle":   errorHandle(cfg),
		"terminal": true,
	})

	return map[string]any{
		"listen":          []string{"tailscale/" + PersonalNodeName(c.ID) + ":443"},
		"automatic_https": map[string]any{"disable": true},
		// One default TLS policy. With cert tags, Caddy picks the cert
		// whose tag matches the SNI / wildcard.
		"tls_connection_policies": []map[string]any{
			{"certificate_selection": map[string]any{"any_tag": []string{c.ID}}},
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
		"Host":                    {"{http.request.host}"},
		"X-Forwarded-Host":        {"{http.request.host}"},
		"X-Forwarded-Proto":       {"https"},
		"X-Tailscale-User":        {"{http.auth.user.tailscale_login}"},
		"X-Tailscale-User-Email":  {"{http.auth.user.tailscale_user}"},
		"X-Tailscale-User-Name":   {"{http.auth.user.tailscale_name}"},
		"X-Tailscale-Node":        {"{http.auth.user.tailscale_node}"},
		"X-Tailscale-Tailnet":     {"{http.auth.user.tailscale_tailnet}"},
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

