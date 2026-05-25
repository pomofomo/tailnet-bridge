# tailnet-bridge (Caddy implementation)

A small per-user reverse proxy that lets your personal Tailscale devices reach
services on one or more shared **community** tailnets — without ever exposing
your personal tailnet back to those communities. One-way membership, enforced
by construction.

This README is everything you need to set up, run, and operate one. If you
want the design rationale or are writing your own implementation, see
[SPEC.md](./SPEC.md).

---

## What this is

You have a personal Tailscale network — your laptop, your phone, your NAS,
your private services. You also belong to one or more **community** tailnets:
a family wiki, a hackerspace's shared tooling, a co-op's internal apps. Those
communities have services you want to reach from your own devices, but you
absolutely do not want the community (or any other member of it) to be able
to reach into your personal tailnet.

The bridge runs as a container on your side and gives you exactly that:

- From any of your personal devices, `https://smithfamily-wiki.<your-tailnet>.ts.net`
  reaches the `wiki` service on the Smith Family community tailnet.
- From the Smith Family community tailnet, nothing can reach anything on your
  personal tailnet. Not the community admin, not other members, not the
  services themselves.
- You can be in multiple communities simultaneously; they don't see each
  other or you.
- Leaving a community is a config edit on your own bridge. The community
  has no ability to modify or shut your bridge down.

Each user runs their own bridge container. There is no central service.

## How it works (architecture)

```
┌─────────────────────────────────────────────────────────────────────┐
│ bridge container (one per user)                                     │
│                                                                     │
│   orchestrator (Go, PID 1)         caddy (xcaddy build)             │
│   ┌─────────────────────┐          ┌───────────────────────────┐    │
│   │ • reads config.yml  │ ──POST→  │ • listens on personal     │    │
│   │ • polls each        │ /load    │   tailnet (one tsnet      │    │
│   │   community's       │          │   node per service)       │    │
│   │   service directory │          │ • dials community         │    │
│   │ • builds Caddy JSON │          │   tailnet (one tsnet      │    │
│   │ • serves the bridge │          │   node per community)     │    │
│   │   error page        │          │ • rewrites Location,      │    │
│   └─────────────────────┘          │   Set-Cookie, body URLs   │    │
│            │                       └───────────────────────────┘    │
└────────────┼────────────────────────────────────────────────────────┘
             │   (all networking through tsnet — nothing exposed)
   personal tailnet     community A tailnet     community B tailnet
   ▲ (listeners only)     ▼ (dialers only)        ▼ (dialers only)
```

Two binaries in one container:

- **orchestrator** is the parent process. It parses your `config.yml`, brings
  up an ephemeral tsnet node per community to fetch that community's service
  directory (a small JSON document the community publishes), merges those
  directories, generates a Caddy JSON config, and POSTs it to Caddy's admin
  API. It also runs the `/__bridge_error` and `/__bridge_status` HTTP
  endpoints.
- **caddy** is built with two plugins:
  [`tailscale/caddy-tailscale`](https://github.com/tailscale/caddy-tailscale)
  for tsnet-backed listeners and dialers, and
  [`caddyserver/replace-response`](https://github.com/caddyserver/replace-response)
  for body URL rewriting.

For each `(community, service)` pair, Caddy spawns one tsnet node on your
personal tailnet whose hostname is `<community-prefix><service-name>`. That
node listens on `:443` with a Tailscale-issued cert. Incoming requests are
reverse-proxied through a per-community dialer node (which is registered on
the community's tailnet but never listens for anything) to the real upstream
service. Response `Location` headers, `Set-Cookie` `Domain=` attributes, and
optionally body URLs are rewritten so the browser sees your personal-side
hostname end to end.

**The asymmetry is structural:** Caddy never opens a listener on a community
tailnet, and never dials anything on your personal tailnet. The plugin lets
us declare which-tailnet-which-direction explicitly, and we only declare the
allowed direction.

## Before you start: prerequisites

You need:

1. **A personal tailnet on hosted Tailscale.** This is what your devices
   already use. Headscale is not supported in v1; `*.ts.net` MagicDNS is
   assumed. Your tailnet should have MagicDNS enabled (it is by default).

2. **A personal-tailnet auth key.** Generate one from
   <https://login.tailscale.com/admin/settings/keys> — choose "Auth keys",
   create a key that is **reusable** (the bridge brings up multiple nodes
   under this key, one per community-service pair), and ideally tagged.
   This key never leaves your machine.

3. **Membership in at least one community tailnet.** Membership is granted
   out-of-band by the community admin: they send you a tailnet auth key for
   their tailnet. You don't need any Tailscale-account-level relationship
   with the community — only the auth key.

4. **The directory URL for each community.** This is an HTTPS URL on the
   community tailnet that returns a JSON listing of services. The community
   admin gives you this; e.g. `https://directory.smithfamily.ts.net/services`.

5. **Docker and docker-compose** on the host you'll run the bridge on. The
   host itself does not need Tailscale installed — all tsnet networking
   happens inside the container.

## Auth keys, summarized

The bridge needs **N+1 auth keys**, where N is the number of communities:

| Key                                   | Generated by   | Used to                                                          |
| ------------------------------------- | -------------- | ---------------------------------------------------------------- |
| **Personal-tailnet auth key** (×1)    | You            | Register one node per community-service pair on *your* tailnet.  |
| **Community auth key** (×N)           | Each community | Register one dialer node (per community) on *their* tailnet, plus one ephemeral poller node the orchestrator uses to fetch the directory. |

All keys should be **reusable** because the bridge brings up multiple nodes
per key. The bridge persists tsnet state under `/var/lib/bridge` so that on
restarts, nodes resume with the same identity and don't need to re-auth.

Keys are injected via environment variables (recommended), a file path, or
inline in `config.yml` (discouraged; the file is on disk).

## Setup walkthrough

### 1. Get the community directory URLs and keys

From each community admin you want to bridge, you need:

- The directory URL (e.g. `https://directory.smithfamily.ts.net/services`).
- A reusable auth key for the community's tailnet.

### 2. Generate your personal auth key

At <https://login.tailscale.com/admin/settings/keys>:

- "Generate auth key"
- "Reusable": **yes**
- "Ephemeral": **no** (we want persistent node identities)
- Pre-authorized: yes (so nodes don't sit in a pending state)
- Expiry: as long as your operational policy allows; renewing is a config
  reload, not a restart

### 3. Clone and configure

```sh
git clone <this repo> && cd tailnet-bridge/caddy
cp config.example.yml config.yml
$EDITOR config.yml
```

A minimal `config.yml`:

```yaml
personal:
  # Each value of auth_key_env is the name of an env var the container will
  # read. The env var itself holds the actual key.
  auth_key_env: PERSONAL_TAILNET_AUTHKEY
  # Optional. Used as the hostname of the community-side dialer nodes
  # (what community admins will see in their tailnet). Defaults to the
  # container hostname if omitted.
  bridge_hostname: alice-bridge

communities:
  - id: smithfamily                # local identifier; alnum + hyphen
    directory_url: https://directory.smithfamily.ts.net/services
    auth_key_env: SMITHFAMILY_AUTHKEY

  - id: austinmakers
    directory_url: https://services.austinmakers.ts.net/directory
    auth_key_env: AUSTINMAKERS_AUTHKEY

# How often to re-poll every community directory. Default 5m.
poll_interval: 5m

# How long to wait for a community tsnet node to come online before
# giving up on that community. Default 60s.
community_join_timeout: 60s
```

Other tunables (with sensible defaults, usually leave alone):

```yaml
state_dir: /var/lib/bridge            # tsnet state; persisted across restarts
caddy_admin_addr: 127.0.0.1:2019      # container-local
orchestrator_error_port: 8081         # container-local
```

The `id` for each community is purely local — it appears only in your
state directory layout and `/__bridge_status` output. It does **not** have
to match anything on the community side.

### 4. Provide the auth keys

Put them in a `.env` file next to `docker-compose.yml`:

```sh
PERSONAL_TAILNET_AUTHKEY=tskey-auth-...
SMITHFAMILY_AUTHKEY=tskey-auth-...
AUSTINMAKERS_AUTHKEY=tskey-auth-...
```

`docker-compose.yml` reads these by name and refuses to start if
`PERSONAL_TAILNET_AUTHKEY` is missing. Adjust the env section of
`docker-compose.yml` to add lines for new communities you add to
`config.yml`.

Alternative for the security-conscious: use `auth_key_file:
/run/secrets/foo` in `config.yml` and mount the secrets in via Docker
secrets / a tmpfs.

### 5. Start it

```sh
docker compose up -d --build
```

First start does the most work: Caddy brings up one tsnet node per
community-service pair on your personal tailnet, each of which has to
register and get a Let's Encrypt cert from Tailscale (~10–30 s each,
parallelized). Watch progress with:

```sh
docker compose logs -f
```

### 6. Visit a service

From any device on your personal tailnet, open the personal-side URL for
the service:

```
https://<community-prefix><service-name>.<your-personal-tailnet>.ts.net
```

For example, if the Smith Family community uses prefix `smithfamily-` and
publishes a service named `wiki`, you would visit
`https://smithfamily-wiki.alice.ts.net` (replacing `alice` with your
personal tailnet name).

The prefix is chosen by the community admin and announced in their
directory — different communities use different prefixes to avoid
collisions on your tailnet.

## Day-to-day operation

### Re-poll all directories

```sh
docker compose kill -s HUP bridge
# or, equivalently:
make reload
```

This skips the `poll_interval` and re-fetches every community's directory
immediately. Useful right after a community admin adds or removes a service.

### Edit config.yml

Edit, then restart:

```sh
docker compose restart bridge
```

A `SIGHUP` only re-polls directories; it does not re-read `config.yml`.

### Check health

```sh
docker compose exec bridge wget -qO- http://127.0.0.1:8081/__bridge_status
# or:
make status
```

Returns JSON like:

```json
{
  "communities": {
    "smithfamily": {
      "last_successful_poll": "2026-05-25T10:42:11Z",
      "etag": "\"abc123\"",
      "current_directory": { "...": "..." }
    },
    "austinmakers": {
      "last_successful_poll": "2026-05-25T10:38:02Z",
      "last_error": "tsnet auth failed",
      "etag": "\"def456\""
    }
  },
  "order": ["austinmakers", "smithfamily"]
}
```

`last_error` is empty when the last poll succeeded; it carries the most
recent failure message otherwise. The previously cached directory is
retained across failures, so services from a temporarily unreachable
community stay listed.

### Add a community

1. Get the directory URL and a fresh auth key from the new community admin.
2. Add an entry under `communities:` in `config.yml`.
3. Add a line to `.env` and to `docker-compose.yml`'s `environment:` block.
4. `docker compose up -d` (no `--build` needed unless code changed).

### Leave a community

Delete its entry from `config.yml` and `docker compose restart bridge`.
The community-side dialer node will deregister itself; the personal-side
listener nodes for that community's services will be removed. Their tsnet
state in `state_dir` is left on disk (harmless) and can be cleaned up
manually if you care.

If the community admin revokes your auth key first, the bridge stays up
but those services will start returning the bridge error page (see below).

## Failure modes — what users see

The bridge is designed so that failures never silently degrade. When
something is broken, you (or whoever opens the URL) gets a clear error page
that names the community and shows the admin's contact info.

| Situation                                  | What happens                                                                                                    |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------- |
| Community revokes your auth key            | Directory polls and reverse-proxy dials both fail; users see the bridge error page with the last error logged. |
| Community directory server is down         | Last good directory is retained; existing services keep working. Error recorded in `/__bridge_status`.          |
| Single upstream service is down            | That one service shows the bridge error page; the rest of the community is unaffected.                         |
| Directory returns invalid JSON             | Bridge keeps the previous good copy and logs the validation error. No partial application.                     |
| Personal-tailnet auth key invalid          | Listener nodes can't come up; Caddy logs and exits; container restarts. Fix the key and restart.                |
| Caddy subprocess crashes                   | Orchestrator exits non-zero; Docker `restart: unless-stopped` brings it back. tsnet state is preserved.         |
| Service-name collision between communities | New config is refused; existing config stays live; the collision is surfaced in `/__bridge_status`.             |

The bridge error page is served from inside the container on
`127.0.0.1:8081`; Caddy's `handle_errors` directive sends users there when
a reverse_proxy fails. The page is generated from the latest known
directory's `community.name` and `community.contact` so users can see who
to ask, not just that something broke.

## What gets exposed

By default, **nothing is exposed outside the container.** All tsnet
networking happens through ephemeral nodes Caddy spawns; the only ports
inside the container (`127.0.0.1:2019` for Caddy admin, `127.0.0.1:8081`
for the status server) are not published.

If you `docker compose exec` into the container you can reach those for
debugging.

## Limitations

Out of scope for this implementation:

- **Non-HTTPS protocols.** SSH, raw TCP, Postgres, etc. cannot pass through
  the bridge — only HTTPS hostname-based routing. Wrap them, or use a
  different mechanism.
- **Path-based routing.** Each service is one hostname; you cannot map
  `/wiki` and `/git` under the same name to different upstreams.
- **Per-user authorization at the bridge.** The bridge forwards Tailscale
  identity headers (`X-Tailscale-User`, etc.) to upstreams so they can do
  their own access control; the bridge itself does not gate anything other
  than "is this request on the personal tailnet".
- **Headscale.** Hosted Tailscale only; uses `*.ts.net` MagicDNS.
- **Funnel / public exposure.** Tailnet-private use only.
- **Multi-user bridges.** Each bridge serves exactly one personal tailnet.
- **Apps that hardcode their hostname** in JavaScript or JSON APIs. The
  bridge rewrites `Location`, `Set-Cookie`, and (optionally) HTML/CSS/JS
  body content, but it cannot rewrite e.g. an OAuth redirect URL stored
  in a database, or a hostname compiled into a SPA bundle as a string
  constant. Most well-behaved self-hosted apps (Gitea, Vaultwarden,
  Nextcloud, Forgejo, …) work; check the per-service
  `rewrite_body` option in your community's directory if a service needs
  body URL rewriting.

The sibling [`wildcard/`](../wildcard) implementation in this repo trades
more community-admin setup work for fewer compatibility caveats; see the
top-level README of the parent repo for the comparison.

## Project layout

```
.
├── SPEC.md                  full design spec
├── CLAUDE.md                contributor orientation
├── Dockerfile               multi-stage build (caddy + orchestrator)
├── docker-compose.yml       reference deployment
├── config.example.yml       annotated config template
├── caddy/
│   └── bootstrap.json       minimal Caddy config (admin API only)
├── orchestrator/            Go source for the orchestrator binary
│   ├── cmd/orchestrator/    process entry
│   └── internal/
│       ├── config/          parses & validates config.yml
│       ├── directory/       fetches & validates community directories
│       ├── caddyconfig/     builds Caddy JSON from merged directories
│       ├── poller/          polling loop + tsnet fetcher
│       ├── caddyproc/       Caddy subprocess management
│       ├── adminclient/     POSTs to Caddy's admin API
│       ├── status/          /__bridge_error and /__bridge_status server
│       └── health/          per-community health tracking
└── Makefile                 build / test / docker shortcuts
```

## Make targets

```sh
make build       # build the orchestrator binary into ./bin (no docker)
make test        # go test ./...
make test-race   # go test -race ./...
make vet         # go vet ./...
make image       # docker compose build
make up          # docker compose up -d --build
make down        # docker compose down (state volume preserved)
make logs        # docker compose logs -f
make reload      # SIGHUP to the bridge: re-poll all directories
make status      # fetch /__bridge_status from inside the container
```

## Going deeper

If you want the design rationale, the threat model, the directory protocol
schema, or want to write a compatible directory server: read
[SPEC.md](./SPEC.md). It is the authoritative document; this README is the
operational layer on top of it.
