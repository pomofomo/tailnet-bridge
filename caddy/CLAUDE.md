# Tailnet Community Bridge

Per-user L7 reverse proxy that lets a Tailscale user's personal devices reach
services on one or more shared "community" tailnets, while strictly preventing
inbound access from those communities back into the user's personal tailnet.

The canonical design document is [SPEC.md](./SPEC.md). Read it first. This file
exists to orient a contributor working in this tree; it does not redefine
behavior.

## Components

The bridge is a single container image with two binaries:

- **`orchestrator`** — small Go program. Owns the lifecycle. Reads
  `config.yml`, polls each community's service directory over a tsnet node it
  joins to that community, generates Caddy JSON config from the merged
  directories, and POSTs it to Caddy's admin API. Also runs the bridge error
  page server.
- **`caddy`** — `xcaddy`-built Caddy with two plugins:
  - `github.com/tailscale/caddy-tailscale` — `bind tailscale/<node>` for
    personal-side listeners, `transport tailscale <node>` for community-side
    dialers. Spawns one tsnet node per personal-side service and one tsnet
    node per community for dialing.
  - `github.com/caddyserver/replace-response` — response body rewriting for
    services whose directory entry sets `rewrite_body: true`.

`orchestrator` is PID 1 in the container and spawns `caddy` as a child. The
admin API is bound to `127.0.0.1:2019`; the error page server to
`127.0.0.1:8081`. Nothing is exposed outside the container — all networking
flows through tsnet.

## Repository Layout

```
.
├── SPEC.md                  spec (authoritative)
├── CLAUDE.md                this file
├── README.md                user-facing quickstart
├── Dockerfile               multi-stage build (caddy + orchestrator)
├── docker-compose.yml       reference compose deployment
├── config.example.yml       annotated example config
├── caddy/
│   └── bootstrap.json       minimal Caddy config: admin API only
└── orchestrator/
    ├── go.mod
    ├── cmd/orchestrator/
    │   └── main.go          process entry; wires components together
    └── internal/
        ├── config/          parses & validates config.yml; resolves secrets
        ├── directory/       fetches & validates community directories (§6)
        ├── caddyconfig/     builds Caddy JSON from merged directories (§8)
        ├── poller/          per-community polling goroutines, SIGHUP, hash
        ├── caddyproc/       manages the Caddy child process
        ├── adminclient/     POSTs config to Caddy's admin API
        ├── status/          /__bridge_error and /__bridge_status server
        └── health/          per-community health tracking shared with status
```

## Key Invariants (from SPEC §2.2, §3, §4)

- **One-way by construction.** Listeners only on the personal tailnet; dialers
  only on community tailnets. Caddy never opens a listener on a community
  tailnet, and never dials anything on the personal tailnet.
- **The directory is authoritative.** The community decides service names,
  prefixes, and body-rewrite behavior. The bridge does not override or infer.
- **Hostname-level routing only.** No path-based or method-based routing.
- **HTTPS on all four legs.** `upstream_scheme: http` is rejected.
- **All upstreams must be subdomains of `community.tailnet`.** Enforced at
  directory-validation time; entire directory is rejected on any violation.
- **User sovereignty.** Personal-tailnet auth keys never leave the user's
  possession. Each bridge serves exactly one personal tailnet.

## Component Contracts (informal)

- `config.Load(path) (*Config, error)` — parse + validate + resolve
  `auth_key_env` / `auth_key_file` / `auth_key`. Rejects unknown fields.
- `directory.Fetch(ctx, httpClient, url, prevETag) (*Directory, etag, status, error)` —
  HTTP GET with `If-None-Match`. Returns `(nil, etag, 304, nil)` on
  unchanged. Validates schema per SPEC §6.2 before returning.
- `caddyconfig.Build(personal Config, dirs map[CommunityID]*Directory) ([]byte, error)` —
  pure function. Deterministic output given the same input. No I/O.
- `adminclient.Load(ctx, addr, jsonConfig) error` — POSTs to
  `http://<addr>/load`. Surfaces Caddy validation errors verbatim.
- `poller.Run(ctx, deps)` — owns the goroutine-per-community loop, the
  config-regen mutex, the SIGHUP fan-out.
- `status.Server` — HTTP server bound to `127.0.0.1:<port>` serving
  `/__bridge_error` (rendered from the health snapshot) and
  `/__bridge_status` (read-only JSON).

## Caddy Generation Notes (SPEC §8)

- For each `(community, service)` pair, declare a **personal-side listener
  node** with hostname `<community.prefix><service.name>` on the personal
  tailnet.
- For each community, declare exactly one **community-side dialer node**
  with hostname `<bridge_hostname>` on the community tailnet.
- Per service site emits a `reverse_proxy` to
  `<upstream_scheme>://<upstream_host>:<upstream_port>` via
  `transport tailscale community-dialer-<id>`.
- `Host` header is set to `{upstream_hostport}` so the upstream sees its own
  canonical name; personal-side hostname is conveyed via `X-Forwarded-Host`.
- `Location` and `Set-Cookie` are always rewritten via `header_down`.
- When `rewrite_body: true`: send `Accept-Encoding: identity` upstream,
  apply `replace-response` for `https://<host>` and `//<host>` plus each
  entry in `rewrite_extra_hosts`, then `encode gzip`.
- `handle_errors` rewrites to `/__bridge_error` and reverse-proxies to
  `http://127.0.0.1:<orchestrator_error_port>`.

## Lifecycle (SPEC §9.1)

1. Parse + validate config.
2. Spawn Caddy with `caddy/bootstrap.json` (admin API only).
3. For each community, bring up a `tsnet.Server` (ephemeral) and fetch its
   directory.
4. Build Caddy JSON, POST to admin `/load`.
5. Loop: poll every `poll_interval`, on `SIGHUP`, or on health-error
   recovery. Regenerate + POST only when the merged config hash changes.
6. On `SIGTERM`/`SIGINT`: forward to Caddy, wait up to 30s, exit.

## Failure Modes (SPEC §10)

- Lost community access → error page on `/__bridge_error`; bridge stays up.
- Caddy crash → orchestrator exits non-zero; container supervisor restarts.
- Service-name collision across communities (same prefix + name) → refuse to
  apply new config; surface in `/__bridge_status`.

## Out of Scope (SPEC §12)

Headscale, non-HTTPS protocols, path-based routing, bridge-side auth/authz,
Funnel, bridge-to-bridge federation, directory mutation, web admin UI.

## Conventions

- Go module path: `bridge`. All internal packages live under
  `bridge/internal/...`; nothing in `internal/` is importable by anything
  outside the orchestrator binary.
- No third-party deps beyond `tailscale.com/tsnet` and `gopkg.in/yaml.v3`
  unless the spec demands more. Stdlib for HTTP, signals, logging, JSON.
- Logging: stderr only. Caddy stdout/stderr are piped through with a
  `caddy: ` prefix.
- Errors travel up; only the top-level loop and the status server decide how
  to surface them.
- Tests live next to the code they exercise. Pure functions
  (`caddyconfig.Build`, directory validation) get table tests; lifecycle
  code gets integration tests against a fake Caddy admin + fake directory
  HTTP server.
