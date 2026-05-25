# Tailnet Community Bridge (Wildcard Domain)

Per-user L7 reverse proxy variant in which **the canonical service hostname is
identical on both sides of the bridge**. Each community owns a DNS subdomain
under `ts.<basedomain>` (e.g. `smithfamily.ts.example.com`), the bridge
terminates TLS with a wildcard cert distributed by the community admin, and
reverse-proxies to the same hostname on the community tailnet. No body or
header rewriting happens, because there is nothing to rewrite.

The canonical design document is [SPEC.md](./SPEC.md). Read it first. This
file orients a contributor working in this tree; it does not redefine
behavior.

The sibling `../caddy/` implementation in this repo solves the same problem
without requiring the community to operate a domain. It rewrites hostnames
in `Location`, `Set-Cookie`, and (optionally) response bodies. The two trees
share architecture and conventions but differ in: cert provenance (this
variant: static PEM files; caddy variant: tsnet-managed Let's Encrypt),
number of personal-side listener nodes (this variant: one per community;
caddy variant: one per service), and whether `replace-response` is built
into the Caddy binary (this variant: no).

## Components

The bridge is a single container image with two binaries:

- **`orchestrator`** — small Go program, PID 1 in the container. Reads
  `config.yml`, polls each community's service directory over an ephemeral
  tsnet node, validates the wildcard cert files on disk, generates Caddy
  JSON config, POSTs it to Caddy's admin API. Also runs (a) a small
  embedded DNS responder that the personal tailnet's Split DNS points at,
  (b) the bridge error/status page server, and (c) a background goroutine
  that periodically checks public DNS for the community domains and warns
  if any of them resolve (the cert trust model demands they do not — see
  SPEC §3.5, §9.5).
- **`caddy`** — `xcaddy`-built Caddy with one plugin:
  - `github.com/tailscale/caddy-tailscale` — `bind tailscale/<node>` for the
    personal-side listener (one node per community), `transport tailscale
    <node>` for community-side dialing (one dialer node per community).

  Note: `caddyserver/replace-response` is intentionally **not** built in.
  No body rewriting is ever needed in this variant.

`orchestrator` spawns `caddy` as a child. The admin API binds
`127.0.0.1:2019`; the error page server binds `127.0.0.1:<port>`
(default 8081); the embedded DNS responder binds UDP/53 on each
community's personal-tailnet listener node. Nothing else is exposed
outside the container — all networking flows through tsnet.

## Repository Layout

```
.
├── SPEC.md                  spec (authoritative)
├── CLAUDE.md                this file
├── README.md                user-facing quickstart
├── Dockerfile               multi-stage build (caddy + orchestrator)
├── docker-compose.yml       reference deployment
├── config.example.yml       annotated example config
├── .env.example             placeholder env vars
├── Makefile                 build / test / docker shortcuts
├── caddy/
│   └── bootstrap.json       minimal Caddy config (admin API only)
├── scripts/                 operator and member conveniences (bash)
│   ├── issue-community-cert.sh
│   ├── setup-community-dns.sh
│   └── setup-personal-split-dns.sh
└── orchestrator/
    ├── go.mod
    ├── cmd/orchestrator/
    │   └── main.go          process entry; wires components together
    └── internal/
        ├── config/          parses & validates config.yml (SPEC §7)
        ├── directory/       fetches & validates directories (SPEC §8)
        ├── cert/            PEM load, SAN/expiry checks, file watcher (SPEC §9.4, §10.1)
        ├── caddyconfig/     builds Caddy JSON (SPEC §9)
        ├── poller/          per-community polling loop, SIGHUP fan-out
        ├── caddyproc/       Caddy child-process lifecycle
        ├── adminclient/     POSTs config to Caddy's admin API
        ├── dns/             embedded UDP/53 responder (SPEC §7.3, §10.1)
        ├── dnscheck/        public-DNS sanity check goroutine (SPEC §9.5)
        ├── status/          /__bridge_error and /__bridge_status server
        └── health/          per-community health tracking (incl. cert expiry)
```

## Why a Bash/Go split (and not all-Go)

The three `scripts/` helpers are **bash** because:

- Each one is a thin wrapper over an existing tool (`lego` for cert issuance,
  `curl` + the Tailscale REST API for the DNS scripts).
- They are operator/member conveniences run ad-hoc from a shell, not
  bridge-runtime code. The bridge never invokes them.
- Bash makes the contract — "this is what calls X to do Y" — obvious to
  someone auditing the script.

Everything that runs inside the bridge container is **Go** for the same
reasons as the caddy variant: tsnet is a Go library, the directory poller
needs goroutines, and we want a single static binary.

The error page is a Go `html/template` rendered by `internal/status`, not a
static HTML file — it interpolates per-community contact info and cert
expiry timestamps from the live health snapshot.

## Key Invariants (from SPEC §2.2, §3, §4)

- **One-way by construction.** Listeners only on the personal tailnet,
  one node per community; dialers only on community tailnets. Caddy
  never opens a listener on a community tailnet, and never dials anything
  on the personal tailnet.
- **The directory is authoritative.** The community decides service names.
  The bridge does not infer or override.
- **The `ts.` subdomain is sacred (SPEC §4.9).** No name under
  `ts.<basedomain>` ever resolves on the public internet. The wildcard
  cert trust model depends on this. `internal/dnscheck` enforces it as
  a runtime warning.
- **Canonical name end-to-end.** `<service>.<community>.ts.<basedomain>`
  is what the upstream emits, what the client requests, and what flows
  through the bridge. The `Host` header is preserved upstream. No rewriting.
- **HTTPS on all four legs.** Caddy terminates with the wildcard, dials
  upstream with TLS verification on (the upstream's cert chain validates
  because the wildcard issuer is a public CA).
- **One listener node per community, not per service.** Wildcard cert
  collapses what would have been many nodes into a small fixed number.
- **Cert material is provisioned out-of-band.** Caddy uses static
  `tls <cert> <key>` directives. No ACME inside the bridge.
- **User sovereignty.** Personal-tailnet auth key never leaves the user.
  Cert files are owned and updated by the user. The community has no
  access to the bridge.

## Component Contracts (informal)

- `config.Load(path) (*Config, error)` — parse + validate + resolve
  `auth_key_env` / `auth_key_file`. Rejects unknown fields. Verifies
  `cert_path` / `key_path` exist and are readable.
- `directory.Fetch(ctx, httpClient, url, prevETag) (*Directory, etag, status, error)` —
  HTTP GET with `If-None-Match`. Returns `(nil, etag, 304, nil)` on
  unchanged. Validates schema per SPEC §8.2 before returning.
- `cert.Load(certPath, keyPath) (*Bundle, error)` — read PEM, verify
  key/cert match, extract SANs, expiry, leaf chain. Pure I/O + parsing.
- `cert.Validate(b *Bundle, expectedDomain string) error` — verify SAN
  covers `*.<expectedDomain>`, expiry > 24h, chain validates against
  system trust store (SPEC §9.4).
- `cert.Watcher` — polls a set of `(certPath, keyPath)` pairs on a
  ticker; emits an event when any file's mtime+content-hash changes.
- `caddyconfig.Build(personal Config, dirs map[CommunityID]*Directory, certs map[CommunityID]*cert.Bundle) ([]byte, error)` —
  pure function. Deterministic output given identical inputs. No I/O.
  One site block per community using its wildcard cert and dispatching
  service hostnames via Host-header matchers (SPEC §9.2).
- `adminclient.Load(ctx, addr, jsonConfig) error` — POSTs to
  `http://<addr>/load`. Surfaces Caddy validation errors verbatim.
- `poller.Run(ctx, deps)` — owns the goroutine-per-community loop, the
  cert-watcher ticker, the config-regen mutex, the SIGHUP fan-out.
- `dns.Server` — UDP/53 responder. Answers `*.<community.domain>` with
  the corresponding community's listener-node IP on the personal
  tailnet. Anything else: REFUSED.
- `dnscheck.Run(ctx, domains, resolver)` — periodically issues an A/AAAA
  query for each community's domain against the host's public resolver
  (bypassing tailnet split DNS). Logs a loud warning on any positive
  answer. Surfaced on `/__bridge_status`.
- `status.Server` — HTTP server bound to `127.0.0.1:<port>` serving
  `/__bridge_error` (rendered from the health snapshot, including cert
  expiry per community) and `/__bridge_status` (read-only JSON).

## Caddy Generation Notes (SPEC §9)

- For each community, declare:
  - one **personal-side listener tsnet node** with hostname
    `<community.id>-bridge` on the personal tailnet,
  - one **community-side dialer tsnet node** with hostname
    `<personal.bridge_hostname>` on the community tailnet.
- Per community, emit ONE site block matching `*.<community.domain>`:
  - `bind tailscale/personal-<community.id>`
  - `tls <cert_path> <key_path>` — static PEM files
  - One `@<service> host <name>.<community.domain>` + `handle @<service>`
    matcher per directory entry, each containing a `reverse_proxy
    https://<upstream_tailnet_host>:<upstream_port>` with
    `transport tailscale community-dialer-<community.id> { tls }`.
  - **`Host` is preserved upstream.** Same name on both sides.
  - Identity headers (`X-Tailscale-User`, `…-User-Email`, `…-User-Name`,
    `…-Node`) set via `header_up` (SPEC §9.2).
  - A catch-all `handle {}` rewrites to `/__bridge_error` for any
    sub-hostname under the wildcard that isn't a known service.
  - `handle_errors` also rewrites to `/__bridge_error`.
- No `header_down Location`. No `header_down Set-Cookie`. No `replace`.
  No `encode`.

## Lifecycle (SPEC §10.3)

1. Parse + validate config.
2. Validate every `cert_path`/`key_path` pair exists, is well-formed,
   chains correctly, and covers the configured domain.
3. Spawn Caddy with `caddy/bootstrap.json` (admin API only).
4. For each community: bring up an ephemeral `tsnet.Server`, fetch the
   directory.
5. Build Caddy JSON, POST to admin `/load`.
6. Loop: poll directories every `poll_interval`; check cert files every
   `cert_check_interval`; re-resolve community domains over public DNS
   every `poll_interval` and warn on any answer; regenerate + POST only
   when the merged config hash changes.
7. On `SIGHUP`: immediate re-poll of everything (directories + certs).
8. On `SIGTERM`/`SIGINT`: forward to Caddy, wait up to 30s, exit.

## Failure Modes (SPEC §12)

- Cert expired or missing for one community → that community is skipped
  in the next config; rest of bridge keeps working; error page for any
  `<svc>.<community>.ts.<base>` reports "Cert expired, contact admin".
- Cert SAN mismatch → same as expired.
- Cert rotated mid-flight → file watcher fires; orchestrator regenerates
  config; Caddy reload is atomic.
- Community revokes member's auth key → `transport tailscale` dial
  fails; `handle_errors` returns the bridge error page.
- Public DNS resolves a `*.ts.<base>` name → loud log + status surface;
  bridge keeps running (it cannot enforce DNS policy on a third party's
  domain).
- Split DNS misconfigured on personal tailnet → personal devices get
  NXDOMAIN; bridge never sees the request. Documented as the first
  thing to verify in troubleshooting.

## Out of Scope (SPEC §13)

Automatic cert distribution, per-service certs, cert pinning,
multiple community-domain roots (mixing `ts.example.com` with
`ts.example.org` in one bridge), Headscale, non-HTTPS protocols,
bridge-to-bridge federation, web admin UI.

## Conventions

- Go module path: `bridge`. All internal packages live under
  `bridge/internal/...`; nothing in `internal/` is importable by anything
  outside the orchestrator binary.
- No third-party deps beyond `tailscale.com/tsnet`, `gopkg.in/yaml.v3`,
  and `github.com/miekg/dns` (already a transitive dep of tsnet, lifted
  to direct for the embedded responder) unless the spec demands more.
- Logging: stderr only. Caddy stdout/stderr piped through with a
  `caddy: ` prefix.
- Errors travel up; only the top-level loop and the status server
  decide how to surface them.
- Tests live next to the code they exercise. Pure functions
  (`caddyconfig.Build`, directory validation, cert SAN matching) get
  table tests; lifecycle code gets integration tests against a fake
  Caddy admin + fake directory HTTP server + fake PEM fixtures.

## Status

This tree currently contains **stubs only**. The package layout,
contracts, and `scripts/` skeleton are in place. Implementations are
intentionally TODO so the spec can be reviewed against a concrete
shape before code is written. Every Go file compiles; every script
runs `--help` and `--dry-run`; no real bridging happens yet.
