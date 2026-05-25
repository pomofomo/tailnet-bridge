# tailnet-bridge (wildcard-domain implementation)

A small per-user reverse proxy that lets your personal Tailscale devices reach
services on one or more shared **community** tailnets — without ever exposing
your personal tailnet back to those communities. One-way membership, enforced
by construction.

This variant uses a **community-owned DNS subdomain** so that the canonical
service hostname is identical on both sides of the bridge. Apps with
hardcoded URLs in JavaScript, OAuth callbacks, or JSON APIs Just Work, at
the cost of the community admin operating a domain and rotating a wildcard
TLS cert.

This README is everything you need to set up, run, and operate one. If you
want the design rationale or are writing your own implementation, see
[SPEC.md](./SPEC.md). The sibling [../caddy/](../caddy) implementation
trades fewer admin requirements for some compatibility caveats; the
[parent repo's README](../README.md) compares the two.

---

## Current status

**Implemented.** The orchestrator builds, the Caddy `bridgedns` plugin
builds, the unit tests pass, and `make smoke` exercises the startup
lifecycle locally. The package framework follows SPEC.md end-to-end.

What's NOT covered by `make smoke` (because they need real
infrastructure):

- Coming up on real personal/community tailnets via tsnet.
- Terminating TLS with a real public-CA wildcard cert.
- The SIGHUP signal path through Docker.
- The personal-tailnet Split DNS routing.

See ["Verify locally"](#verify-locally) below for the safe checks you
can run on any machine, and ["Setup walkthrough"](#setup-walkthrough-member-side)
for the real-deploy path.

---

## What this is

You have a personal Tailscale network — your laptop, your phone, your NAS,
your private services. You also belong to one or more **community**
tailnets: a family wiki, a hackerspace's shared tooling, a co-op's internal
apps. Those communities have services you want to reach from your own
devices, but you absolutely do not want the community (or any other
member of it) to be able to reach into your personal tailnet.

The bridge runs as a container on your side and gives you exactly that:

- From any of your personal devices, `https://wiki.smithfamily.ts.example.com`
  reaches the `wiki` service on the Smith Family community tailnet.
- From the Smith Family community tailnet, nothing can reach anything on
  your personal tailnet. Not the community admin, not other members, not
  the services themselves.
- You can be in multiple communities simultaneously; they don't see each
  other or you.
- Leaving a community is a config edit on your own bridge (and the
  community admin stops sending you new wildcard certs). The community
  has no ability to modify or shut your bridge down.

Each user runs their own bridge container. There is no central service.

## How it works (architecture)

```
┌─────────────────────────────────────────────────────────────────────┐
│ bridge container (one per user)                                     │
│                                                                     │
│   orchestrator (Go, PID 1)         caddy (xcaddy + tailscale)       │
│   ┌─────────────────────┐          ┌───────────────────────────┐    │
│   │ • reads config.yml  │ ──POST→  │ • listens on personal     │    │
│   │ • polls each        │ /load    │   tailnet (ONE tsnet node │    │
│   │   community's       │          │   per community)          │    │
│   │   service directory │          │ • dials community         │    │
│   │ • validates &       │          │   tailnet (one tsnet      │    │
│   │   watches cert/key  │          │   node per community)     │    │
│   │   files on disk     │          │ • terminates TLS with the │    │
│   │ • builds Caddy JSON │          │   community's wildcard    │    │
│   │ • answers personal- │          │   cert from disk          │    │
│   │   tailnet split DNS │          │ • Host header PRESERVED   │    │
│   │ • serves the bridge │          │   upstream                │    │
│   │   error/status page │          └───────────────────────────┘    │
│   │ • warns if any      │                                           │
│   │   *.ts.example.com  │                                           │
│   │   name resolves on  │                                           │
│   │   public DNS        │                                           │
│   └─────────────────────┘                                           │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
              │   (all networking through tsnet — nothing exposed)
    personal tailnet     community A tailnet     community B tailnet
    ▲ (listeners only)     ▼ (dialers only)        ▼ (dialers only)
```

Two binaries in one container:

- **orchestrator** is the parent process. It parses `config.yml`, brings
  up an ephemeral tsnet node per community to fetch that community's
  service directory, validates the wildcard cert files you placed on
  disk, generates a Caddy JSON config, and POSTs it to Caddy's admin
  API. It also runs `/__bridge_error` and `/__bridge_status` over loopback,
  and a small UDP/53 responder per community that the personal-tailnet
  split-DNS configuration points at.
- **caddy** is built with one plugin:
  [`tailscale/caddy-tailscale`](https://github.com/tailscale/caddy-tailscale)
  for tsnet-backed listeners and dialers. **No** body-rewrite plugin is
  needed (or built in) — the canonical hostname is the same on both
  sides of the bridge.

For each community, Caddy spawns exactly **one** tsnet node on your
personal tailnet (not one per service). That node listens on `:443`
serving `*.<community>.ts.<basedomain>` with the wildcard cert. Host-header
matchers dispatch each `<service>.<community>.ts.<basedomain>` request to
the matching upstream on the community tailnet via a per-community dialer
node.

**The asymmetry is structural:** Caddy never opens a listener on a
community tailnet, and never dials anything on your personal tailnet.
The plugin lets us declare which-tailnet-which-direction explicitly,
and we only declare the allowed direction.

## Before you start: prerequisites

You need:

1. **A personal tailnet on hosted Tailscale.** Headscale is not supported
   in v1. Your tailnet should have MagicDNS enabled.

2. **A personal-tailnet auth key**, reusable, non-ephemeral, pre-authorized.
   Generate one from <https://login.tailscale.com/admin/settings/keys>.
   The bridge will bring up one node per community under this key.

3. **Membership in at least one community tailnet.** Membership is granted
   out-of-band by the community admin: they send you (a) an auth key for
   the community tailnet, (b) the wildcard TLS cert + key for the
   community's domain, (c) the directory URL, and (d) the community's
   subdomain (e.g. `smithfamily.ts.example.com`).

4. **Docker and docker-compose** on the host you'll run the bridge on. The
   host itself does not need Tailscale installed — tsnet runs inside the
   container.

5. **`dns:write`-scoped Tailscale OAuth client** for your personal tailnet
   *only if* you want to script the split-DNS setup with
   `scripts/setup-personal-split-dns.sh`. Otherwise you can configure it
   once in the Tailscale admin console.

The community admin also has work to do — see the [community admin's
runbook](#for-community-admins) below.

## Auth keys and certs, summarized

The bridge needs **N+1 auth keys and N cert pairs**, where N is the number
of communities:

| Material                              | Generated by   | Used to                                                                  |
| ------------------------------------- | -------------- | ------------------------------------------------------------------------ |
| **Personal-tailnet auth key** (×1)    | You            | Register one listener node per community on *your* tailnet.              |
| **Community auth key** (×N)           | Each community | Register one dialer node (per community) on *their* tailnet, plus one ephemeral poller node the orchestrator uses to fetch the directory. |
| **Wildcard cert + key** (×N)          | Each community | Terminate TLS for `*.<community>.ts.<basedomain>`. Files placed at `cert_path` / `key_path` in `config.yml`. |

All auth keys should be **reusable** because the bridge brings up
multiple nodes per key. Cert files should be `0600`, owned by the user
the bridge runs as.

The bridge persists tsnet state under `/var/lib/bridge` so that on
restarts, nodes resume with the same identity and don't need to re-auth.

## Verify locally

Before touching real tailnets or certs, you can verify the build is
sane on any machine:

```sh
make check          # go vet + go build + go test on orchestrator and plugin
make test-race      # same tests, with the race detector
make smoke          # binary-level lifecycle smoke test (no tailnets)
```

Tests cover:

- `internal/config`: YAML parsing, defaults, domain-shape rules, mixed
  base-domain rejection, auth-key resolution.
- `internal/directory`: SPEC §8.2 schema validation; ETag-based 304
  handling; rejection of unknown fields.
- `internal/cert`: PEM load (RSA + ECDSA), key-cert match check,
  expiry / SAN coverage, the rotation watcher.
- `internal/caddyconfig`: deterministic JSON output; per-community
  one-listener structure; SNI override on the transport; cert
  load_files; bridgedns app entries; collision behavior.
- `internal/health`: per-community snapshot store; domain-based
  hostname attribution; DNS-leak tracking.
- `internal/poller`: per-community fetch loop, cert-watcher reload,
  apply-on-change hash dedupe, "preserve prior directory on error".
- `internal/dnscheck`: NXDOMAIN ≠ violation, positive answer ⇒
  violation, multi-domain fan-out.
- `internal/status`: `/__bridge_error` rendering for known / unknown
  communities, cert-expiry diagnosis, DNS-leak warning surface.
- `caddy-plugin/bridgedns`: A/AAAA answers, apex NXDOMAIN, out-of-zone
  REFUSED, case-insensitive matching, IPv4/IPv6 split.

`make smoke` additionally exercises:

- A real `bin/orchestrator` startup with `BRIDGE_CONFIG` set, verifying
  it parses the config and reaches the "wait for Caddy admin" step
  with a clear error (rather than panicking or silently exiting).
- `scripts/*.sh` syntax, `--help`, and `--dry-run` modes.

If any of these fail, the deploy WILL fail. They're cheap; run them
after every config change.

## Setup walkthrough (member side)

### 1. Collect what the community admin sent you

For each community you want to bridge, you should have:

- The community's **subdomain** (e.g. `smithfamily.ts.example.com`).
- The community's **directory URL** (e.g.
  `https://directory.smithfamily.ts.example.com/services`).
- A **community-tailnet auth key**.
- The **wildcard cert + key** (`cert.pem` and `key.pem`) for
  `*.<community>.ts.example.com`.

### 2. Generate your personal-tailnet auth key

At <https://login.tailscale.com/admin/settings/keys>:

- "Generate auth key"
- Reusable: **yes**
- Ephemeral: **no**
- Pre-authorized: **yes**
- Expiry: as long as your operational policy allows.

### 3. Clone and configure

```sh
git clone <this repo> && cd tailnet-bridge/wildcard
cp config.example.yml config.yml
cp .env.example .env
$EDITOR config.yml .env
```

Three things to edit:

- `config.yml` — what communities you belong to, where their cert files
  live on disk, and which env var holds each auth key.
- `.env` — the actual auth-key values. Never committed.
- `docker-compose.yml`'s `environment:` block — add the matching
  variable name for each community you add.

A minimal `config.yml`:

```yaml
personal:
  auth_key_env: PERSONAL_TAILNET_AUTHKEY
  bridge_hostname: alice-bridge

communities:
  - id: smithfamily
    domain: smithfamily.ts.example.com
    directory_url: https://directory.smithfamily.ts.example.com/services
    auth_key_env: SMITHFAMILY_AUTHKEY
    cert_path: /etc/bridge/certs/smithfamily/cert.pem
    key_path:  /etc/bridge/certs/smithfamily/key.pem

poll_interval: 5m
cert_check_interval: 1m
community_join_timeout: 60s
state_dir: /var/lib/bridge
caddy_admin_addr: 127.0.0.1:2019
orchestrator_error_port: 8081
```

### 4. Place the wildcard cert and key

The bridge mounts a host directory at `/etc/bridge/certs` (see
`docker-compose.yml`). Lay it out one directory per community:

```
./certs/
├── smithfamily/
│   ├── cert.pem    # owner-readable only; mode 0600
│   └── key.pem
└── austinmakers/
    ├── cert.pem
    └── key.pem
```

When the community admin rotates and re-sends you the cert (typically
every 14 days; see SPEC §6.3), overwrite these files in place. The
orchestrator notices the change within `cert_check_interval` (default
1 min) and tells Caddy to reload — no restart needed.

### 5. Fill in `.env`

```sh
PERSONAL_TAILNET_AUTHKEY=tskey-auth-...
SMITHFAMILY_AUTHKEY=tskey-auth-...
# AUSTINMAKERS_AUTHKEY=tskey-auth-...
```

`docker-compose.yml` refuses to start (and `make image` refuses to
build) if `PERSONAL_TAILNET_AUTHKEY` is missing — that hard fail is
intentional.

### 6. Configure personal-tailnet split DNS

This is **required** for every community: your personal tailnet must
route `<community>.ts.example.com` lookups to the bridge's
per-community listener IP.

Two options:

**Option A — manual (recommended for first-time setup):**

1. Open <https://login.tailscale.com/admin/dns> for your personal tailnet.
2. Under "Nameservers," click "Add nameserver" → "Custom."
3. Restrict to a domain: `smithfamily.ts.example.com`.
4. Nameserver address: the bridge's personal-tailnet IP (visible after
   first start in `make logs` or the Tailscale admin console).
5. Repeat for each community.

**Option B — scripted:**

```sh
./scripts/setup-personal-split-dns.sh \
  --community-domain smithfamily.ts.example.com \
  --bridge-tailnet-ip 100.64.0.42 \
  --api-key tskey-api-... \
  --dry-run                       # remove --dry-run to actually apply
```

Requires a Tailscale OAuth client with `dns:write` scope. See
`scripts/setup-personal-split-dns.sh --help`.

### 7. Start the bridge

```sh
docker compose up -d --build
make logs
```

First start: orchestrator validates cert files, brings up the ephemeral
poller node per community, fetches each directory, builds Caddy config,
brings up the listener and dialer nodes. Expect ~10–30 s of tsnet
auth + connection setup per community.

### 8. Visit a service

From any device on your personal tailnet, visit the canonical URL:

```
https://<service>.<community>.ts.example.com
```

For example, if the Smith Family community publishes a `wiki` service,
visit `https://wiki.smithfamily.ts.example.com` from your laptop, your
phone, anywhere on your personal tailnet. The cert was issued by Let's
Encrypt (or whatever public CA the community admin chose), so your
browser shows a green padlock with no special trust setup.

## End-to-end deployment flow at a glance

```
┌──────────────────────┐      ┌─────────────────────────────────────┐
│  Community admin     │      │  You (member)                       │
├──────────────────────┤      ├─────────────────────────────────────┤
│ 1. Own a domain      │      │                                     │
│    ts.example.com    │      │                                     │
│ 2. Run a directory   │      │                                     │
│    server on the     │      │                                     │
│    community tailnet │      │                                     │
│ 3. Configure         │      │                                     │
│    community-side    │      │                                     │
│    split DNS         │      │                                     │
│    (scripts/setup-   │      │                                     │
│     community-dns.sh)│      │                                     │
│ 4. Issue wildcard    │      │                                     │
│    cert with lego    │      │                                     │
│    (scripts/issue-   │      │                                     │
│     community-cert.  │      │                                     │
│     sh)              │      │                                     │
│ 5. Hand off ──┐      │      │                                     │
│    • auth key │      │      │                                     │
│    • cert+key │ ────▶│ 6. Place certs in ./certs/<community>/     │
│    • dir url  │      │ 7. Edit config.yml + .env                  │
│    • domain   │      │ 8. Configure personal split DNS            │
│               │      │    (scripts/setup-personal-split-dns.sh)   │
│               │      │ 9. docker compose up -d --build            │
│ 6. Rotate cert       │      │ 10. Visit                                  │
│    every 14d, re-    │      │     https://<svc>.<community>.ts.example.com │
│    distribute  ─────▶│ 11. Overwrite ./certs/<community>/*.pem    │
│                      │     (bridge auto-reloads)                  │
└──────────────────────┘      └─────────────────────────────────────┘
```

## Day-to-day operation

### Re-poll all directories and re-check all certs

```sh
docker compose kill -s HUP bridge
# or:
make reload
```

This skips both `poll_interval` and `cert_check_interval` and re-runs
everything immediately. Useful right after a community admin adds a
service or rotates the cert.

### Edit `config.yml`

Edit, then restart:

```sh
docker compose restart bridge
```

`SIGHUP` only re-polls; it does not re-read `config.yml`.

### Check health

```sh
make status
# or, equivalently:
docker compose exec bridge wget -qO- http://127.0.0.1:8081/__bridge_status
```

Returns JSON with per-community last poll time, last error, current
directory, cert expiry timestamp, and a `public_dns_warnings` array
listing any `*.ts.<base>` names that surprisingly resolve on the
public internet (see SPEC §3.5).

### Add a community

1. Get auth key, cert + key, directory URL, and domain from the new
   community admin.
2. Place cert/key under `./certs/<id>/`.
3. Add an entry under `communities:` in `config.yml`.
4. Add a line to `.env` and to `docker-compose.yml`'s `environment:` block.
5. Configure personal-tailnet split DNS for the new domain.
6. `docker compose up -d` (no `--build` needed unless code changed).

### Leave a community

Delete its entry from `config.yml` and `docker compose restart bridge`.
The community-side dialer node deregisters; the personal-side listener
node for that community is removed; cert files can be deleted at your
leisure. Their tsnet state in `state_dir` is left on disk (harmless) and
can be cleaned manually if you care.

If the community admin removes you first, the bridge stays up but
service URLs start returning the bridge error page within 14 days (when
the next un-distributed cert rotation makes your old cert expire and
TLS handshakes start failing). Until then, `transport tailscale` dial
failures (because your community auth key was revoked) yield the same
error page sooner.

## Failure modes — what users see

The bridge is designed so that failures never silently degrade. When
something is broken, the URL produces a clear error page that names the
community, the kind of failure, and the admin's contact info (read from
the directory's `community.contact` field).

| Situation                                  | What happens                                                                                                    |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------- |
| Community revokes your auth key            | `transport tailscale` dials fail; users see the bridge error page with the failure logged.                      |
| Community directory server is down         | Last good directory is retained; existing services keep working.                                                |
| Single upstream service is down            | That one service shows the bridge error page; the rest of the community is unaffected.                          |
| Wildcard cert expired                      | Bridge skips that community in the new config; error page reports "Cert for this community has expired — contact admin." |
| Wildcard cert rotated                      | File watcher fires; Caddy reload is atomic; no client-visible interruption.                                     |
| Cert SAN doesn't match `domain`            | Same as expired: community is skipped, error page explains.                                                     |
| Personal-tailnet split DNS misconfigured   | Personal devices get NXDOMAIN; bridge never sees the request. First thing to verify in any "nothing works."     |
| A `*.ts.<base>` name resolves publicly     | Loud warning in logs and on `/__bridge_status`. Bridge keeps running. This is an operator-side problem.         |
| Directory returns invalid JSON             | Bridge keeps the previous good copy and logs the validation error. No partial application.                     |
| Personal-tailnet auth key invalid          | Listener nodes can't come up; container exits non-zero; Docker `restart: unless-stopped` retries.               |
| Caddy subprocess crashes                   | Orchestrator exits non-zero; Docker brings it back. tsnet state and certs preserved.                            |

## What gets exposed

By default, **nothing is exposed outside the container.** All tsnet
networking happens through nodes the bridge spawns; the only ports
inside the container (`127.0.0.1:2019` for Caddy admin, `127.0.0.1:8081`
for the status server) are not published. UDP/53 inside the container is
bound to the per-community tsnet listener interface, not the host.

`docker compose exec` into the container if you want to poke at those
for debugging.

## For community admins

If you're standing up a community for the first time, see
[SPEC.md §6](./SPEC.md#6-the-community-operators-responsibilities) for
the full runbook. In short:

1. **Own a domain.** Pick a base domain you control (`example.com`) and
   commit, by DNS policy, that nothing under `ts.example.com` ever
   resolves on the public internet. See SPEC §3.6 — this is the
   invariant the wildcard cert trust model rests on.
2. **Pick a subdomain per community** under `ts.<base>`:
   `smithfamily.ts.example.com`, etc.
3. **Run a directory server** on the community tailnet. It serves a
   single JSON document over HTTPS describing the community's services.
   See SPEC §8 for the schema.
4. **Configure community-side split DNS** so that
   `<service>.<community>.ts.<base>` resolves to the upstream service's
   community-tailnet IP from inside the community tailnet:

   ```sh
   ./scripts/setup-community-dns.sh \
     --community-domain smithfamily.ts.example.com \
     --tailnet smithfamily.ts.net \
     --api-key tskey-api-... \
     --resolver-ip 100.64.0.10 \
     --dry-run
   ```

5. **Issue the wildcard cert** with a DNS-01 ACME client like `lego`:

   ```sh
   ./scripts/issue-community-cert.sh \
     --domain smithfamily.ts.example.com \
     --provider cloudflare \
     --email admin@example.com \
     --out ./certs/smithfamily/
   ```

6. **Distribute the cert and key** to each member out-of-band (private
   file share, encrypted email, tailnet-only HTTPS endpoint, …). The
   distribution list is the source of truth for membership.

7. **Rotate every 14 days** and re-distribute. A member you stop
   sending the new cert to loses access on the next rotation — that's
   the membership cutoff mechanism. Combined with revoking their
   community-tailnet auth key, the cutoff is immediate at the network
   layer and complete at the TLS layer within 14 days.

All three scripts under `scripts/` accept `--dry-run` and `--help`.

## Limitations

Out of scope for v1:

- **Non-HTTPS protocols.** Only HTTPS reverse-proxying.
- **Path-based routing.** Each service is one hostname.
- **Per-user authorization at the bridge.** The bridge forwards
  Tailscale identity headers; upstreams enforce their own access control.
- **Headscale.** Hosted Tailscale only.
- **Funnel / public exposure.** Tailnet-private use only.
- **Multi-user bridges.** Each bridge serves exactly one personal tailnet.
- **Automatic cert distribution.** The community admin distributes
  certs manually (or via a tailnet-only HTTPS endpoint they run).
- **Multiple base domains.** All communities must share the same `ts.<base>`
  parent. Mixing `ts.example.com` with `ts.example.org` in one bridge
  is not supported.
- **Cert pinning or revocation lists.** Standard public-CA trust only.

The sibling [`../caddy/`](../caddy) implementation in this repo trades
some compatibility (apps with hardcoded hostnames may need body
rewriting, which doesn't always work cleanly) for much less
community-admin setup work; see the parent repo's README for the
comparison.

## Project layout

```
.
├── SPEC.md                  full design spec (read this first)
├── CLAUDE.md                contributor orientation
├── README.md                this file
├── Dockerfile               multi-stage build (caddy + orchestrator)
├── docker-compose.yml       reference deployment
├── config.example.yml       annotated config template
├── .env.example             auth-key placeholder env file
├── Makefile                 build / test / docker shortcuts
├── caddy/
│   └── bootstrap.json       minimal Caddy config (admin API only)
├── caddy-plugin/
│   └── bridgedns/           Caddy app plugin: UDP/53 DNS responder bound to
│                            the same tsnet node Caddy uses for HTTPS
├── scripts/
│   ├── issue-community-cert.sh     (admin) issue a wildcard cert via lego
│   ├── setup-community-dns.sh      (admin) configure community split DNS
│   ├── setup-personal-split-dns.sh (member) configure personal split DNS
│   └── smoke.sh                    local lifecycle smoke test (no tailnets)
└── orchestrator/            Go source for the orchestrator binary
    ├── cmd/orchestrator/    process entry
    └── internal/
        ├── config/          parses & validates config.yml
        ├── directory/       fetches & validates community directories
        ├── cert/            wildcard cert load, validation, file watch
        ├── caddyconfig/     builds Caddy JSON from merged directories
        ├── poller/          polling + cert-watch loop
        ├── caddyproc/       Caddy subprocess management
        ├── adminclient/     POSTs to Caddy's admin API
        ├── dnscheck/        public-DNS sanity goroutine
        ├── status/          /__bridge_error and /__bridge_status server
        └── health/          per-community health tracking
```

## Make targets

```sh
make build       # build the orchestrator binary into ./bin (no docker)
make test        # go test for orchestrator + plugin
make test-race   # tests with race detector
make vet         # go vet across both modules
make check       # vet + build + test (run before commits)
make smoke       # binary-level lifecycle smoke (no tailnets)
make image       # docker compose build
make up          # docker compose up -d --build
make down        # docker compose down (state volume preserved)
make logs        # docker compose logs -f
make reload      # SIGHUP to the bridge: re-poll + re-check certs
make status      # fetch /__bridge_status from inside the container
```

## Going deeper

If you want the design rationale, the threat model, the wildcard-cert
trust argument, or the directory protocol schema: read
[SPEC.md](./SPEC.md). It is the authoritative document; this README
is the operational layer on top of it.
