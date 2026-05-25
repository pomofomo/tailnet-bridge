// Package caddyconfig builds the Caddy JSON config (SPEC §8) from the
// bridge config plus the merged set of community directories.
//
// Build is a pure function: given identical inputs it must produce
// byte-identical output. This invariant is what lets the poller compare
// hashes to decide whether a reload is needed.
package caddyconfig

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"bridge/internal/config"
	"bridge/internal/directory"
)

// Input bundles everything Build needs. The map key is the community ID
// from config.yml (NOT the directory's community.name).
type Input struct {
	Config      *config.Config
	Directories map[string]*directory.Directory
}

// Build returns the JSON payload to POST to Caddy's /load endpoint.
//
// Collisions: if two (community, service) pairs produce the same
// personal-side hostname (same prefix+name), Build returns an error and
// the caller refuses the reload (SPEC §10.7).
func Build(in Input) ([]byte, error) {
	if in.Config == nil {
		return nil, fmt.Errorf("caddyconfig: nil config")
	}
	cfg := in.Config

	// Stable iteration: sort communities by ID; services within each
	// community sorted by name. This is what gives Build determinism.
	commIDs := make([]string, 0, len(in.Directories))
	for _, c := range cfg.Communities {
		if _, ok := in.Directories[c.ID]; !ok {
			continue
		}
		commIDs = append(commIDs, c.ID)
	}
	sort.Strings(commIDs)
	authByID := make(map[string]string, len(cfg.Communities))
	for _, c := range cfg.Communities {
		authByID[c.ID] = c.AuthKey
	}

	// Detect cross-community personal-side hostname collisions.
	type origin struct{ community, service string }
	hostnames := make(map[string]origin)

	// Build the tailscale nodes map.
	nodes := map[string]any{}
	// Build the http servers map.
	servers := map[string]any{}

	for _, cid := range commIDs {
		dir := in.Directories[cid]
		// Community-side dialer node.
		dialerName := "community-dialer-" + cid
		nodes[dialerName] = map[string]any{
			"auth_key":  authByID[cid],
			"hostname":  cfg.Personal.BridgeHostname,
			"state_dir": filepath.Join(cfg.StateDir, dialerName),
		}

		svcs := make([]directory.Service, len(dir.Services))
		copy(svcs, dir.Services)
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })

		for _, svc := range svcs {
			personalHost := dir.Community.Prefix + svc.Name
			if prev, dup := hostnames[personalHost]; dup {
				return nil, fmt.Errorf(
					"caddyconfig: personal-side hostname collision %q: communities %s/%s and %s/%s share prefix+name",
					personalHost, prev.community, prev.service, cid, svc.Name)
			}
			hostnames[personalHost] = origin{community: cid, service: svc.Name}

			nodeName := "personal-" + cid + "-" + svc.Name
			nodes[nodeName] = map[string]any{
				"auth_key":  cfg.Personal.AuthKey,
				"hostname":  personalHost,
				"state_dir": filepath.Join(cfg.StateDir, nodeName),
			}

			servers[nodeName] = buildServer(cfg, cid, dir, svc, nodeName, dialerName)
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
				"nodes": nodes,
			},
			"http": map[string]any{
				"servers": servers,
			},
		},
	}

	return json.MarshalIndent(out, "", "  ")
}

// buildServer emits one apps.http.servers[<nodeName>] entry.
//
// Listener: "tailscale+tls/<nodeName>:443" — TLS terminates at the
// tsnet listener using the node's tailnet-issued cert, so we disable
// Caddy's automatic_https for this server.
func buildServer(cfg *config.Config, cid string, dir *directory.Directory, svc directory.Service, nodeName, dialerName string) map[string]any {
	upstreamHostPort := svc.UpstreamHost + ":" + strconv.Itoa(svc.UpstreamPort)

	// Request headers ----------------------------------------------------
	reqSet := map[string][]string{
		// Identity (populated by the tailscale auth provider).
		"X-Tailscale-User":       {"{http.auth.user.tailscale_login}"},
		"X-Tailscale-User-Email": {"{http.auth.user.tailscale_user}"},
		"X-Tailscale-User-Name":  {"{http.auth.user.tailscale_name}"},
		"X-Tailscale-Tailnet":    {"{http.auth.user.tailscale_tailnet}"},
		// Forwarding.
		"Host":              {"{http.reverse_proxy.upstream.hostport}"},
		"X-Forwarded-Host":  {"{http.request.host}"},
		"X-Forwarded-Proto": {"https"},
	}
	if svc.RewriteBody {
		// replace_response can't decompress; force identity upstream.
		reqSet["Accept-Encoding"] = []string{"identity"}
	}

	// Response header rewriting -----------------------------------------
	respReplace := map[string][]map[string]string{
		"Location": {
			{
				"search_regexp": "^https?://" + regexp.QuoteMeta(svc.UpstreamHost) + `(?::\d+)?`,
				"replace":       "https://{http.request.host}",
			},
		},
		"Set-Cookie": {
			{
				"search_regexp": `;\s*[Dd]omain=` + regexp.QuoteMeta(svc.UpstreamHost),
				"replace":       "",
			},
		},
	}

	rpHeaders := map[string]any{
		"request": map[string]any{
			"set": reqSet,
		},
		"response": map[string]any{
			"replace": respReplace,
		},
	}

	// Reverse-proxy handler ---------------------------------------------
	reverseProxy := map[string]any{
		"handler": "reverse_proxy",
		"transport": map[string]any{
			"protocol": "tailscale",
			"name":     dialerName,
			"tls":      map[string]any{},
		},
		"upstreams": []map[string]any{
			{"dial": upstreamHostPort},
		},
		"headers": rpHeaders,
	}

	// Authentication middleware: populates {http.auth.user.tailscale_*}
	// placeholders consumed by the request-header set above.
	auth := map[string]any{
		"handler": "authentication",
		"providers": map[string]any{
			"tailscale": map[string]any{},
		},
	}

	// Handler chain for the route. Order matters: response wrappers added
	// later are inner. We want body-rewrite to see decompressed text and
	// the client to receive (optionally) gzip-recompressed output, so:
	//   encode (outermost, recompresses)
	//   replace_response (rewrites text)
	//   authentication
	//   reverse_proxy
	handle := []map[string]any{}
	if svc.RewriteBody {
		handle = append(handle, map[string]any{
			"handler": "encode",
			"encodings": map[string]any{
				"gzip": map[string]any{},
			},
		})
		handle = append(handle, buildReplaceResponse(dir, svc))
	}
	handle = append(handle, auth)
	handle = append(handle, reverseProxy)

	routes := []map[string]any{
		{"handle": handle},
	}

	// Error sub-routes: any upstream failure is rewritten to /__bridge_error
	// and reverse-proxied to the orchestrator's status server.
	errorHandle := []map[string]any{
		{"handler": "rewrite", "uri": "/__bridge_error"},
		{
			"handler": "reverse_proxy",
			"upstreams": []map[string]any{
				{"dial": "127.0.0.1:" + strconv.Itoa(cfg.OrchestratorErrorPort)},
			},
		},
	}

	server := map[string]any{
		"listen":          []string{"tailscale+tls/" + nodeName + ":443"},
		"automatic_https": map[string]any{"disable": true},
		"routes":          routes,
		"errors": map[string]any{
			"routes": []map[string]any{
				{"handle": errorHandle},
			},
		},
	}
	return server
}

// buildReplaceResponse emits the replace_response handler for one
// service. Substring-only (placeholders work in substring mode but not
// in regex mode per the replace-response README).
func buildReplaceResponse(dir *directory.Directory, svc directory.Service) map[string]any {
	hosts := []string{svc.UpstreamHost}
	hosts = append(hosts, svc.RewriteExtraHosts...)
	sort.Strings(hosts)

	replacements := make([]map[string]any, 0, 2*len(hosts))
	for _, h := range hosts {
		replacements = append(replacements,
			map[string]any{
				"search":  "https://" + h,
				"replace": "https://{http.request.host}",
			},
			map[string]any{
				"search":  "//" + h,
				"replace": "//{http.request.host}",
			},
		)
	}
	return map[string]any{
		"handler":      "replace_response",
		"replacements": replacements,
	}
}
