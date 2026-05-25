# Tailscale Community Bridge — Specification

## 1. Background

This document specifies the design and implementation of a **community bridge**: a per-user process that allows a Tailscale user's personal devices to reach services on one or more shared "community" tailnets, while strictly preventing inbound access from those communities back into the user's personal tailnet.

The bridge is an L7 (HTTPS) reverse proxy that joins both the user's personal tailnet and each community tailnet they belong to, exposes community services as nodes on the personal tailnet, and forwards requests through to the real services.

## 2. Core Requirements

These were established in earlier design discussions and must be preserved.

### 2.1 Topology

- Each user has a **personal tailnet** containing their own devices and private services.
- A **community tailnet** is a separate tailnet shared among multiple users; it contains services that members of the community can access.
- A user may belong to **zero or more** community tailnets at once. The bridge supports bridging to multiple communities from a single personal tailnet.
- Community membership is granted by an admin out-of-band (the admin shares an auth key for the community tailnet with the user).

### 2.2 Access Rules

The bridge must enforce these access invariants:

- **Allow:** Devices on a personal tailnet can initiate connections to services on any community tailnet the user is a member of.
- **Reject:** Nothing on a community tailnet can initiate a connection to anything on a personal tailnet (no inbound, even for community admins).
- **Reject:** A user's personal devices cannot reach another user's personal tailnet through any path.
- **Reject:** A user's bridge cannot be used by other personal-tailnet members to reach the community (each bridge is bound to exactly one personal tailnet).
- **Allow:** Personal-to-personal traffic within a single user's own tailnet is unaffected.
- **Allow:** Community-to-community traffic within a single community tailnet is unaffected.

When a user is removed from the community (their auth key revoked or their node deregistered), their personal devices can no longer reach community services. The bridge itself remains running on the personal tailnet but returns a clear error page (see §10). The community has no ability to shut down or modify the user's bridge.

### 2.3 Naming and Discovery

- Community services are reached from personal devices by **MagicDNS hostnames on the personal tailnet**, with a community-specific prefix.
- Example: a wiki service in the "smithfamily" community is reached as `https://smithfamily-wiki.<personal-tailnet>.ts.net`.
- The prefix is chosen by the community admin and announced in the service directory. Different communities use different prefixes so they don't collide on one user's personal tailnet.
- All traffic is HTTPS end to end. Personal-side certs are issued automatically via tsnet's Let's Encrypt integration. Community-side services already have their own tsnet-issued certs.

## 3. Design Choices (Locked In)

The following are settled design decisions; they should not be revisited without explicit cause.

### 3.1 L7 Reverse Proxy, Not L4

The bridge operates at HTTP, not at the IP layer. This was chosen over IP-level subnet routing because:

- Subnet routing across tailnets requires either 4via6 (only available with full `tailscaled`, not `tsnet`) or a userspace packet forwarder. Both significantly complicate the bridge.
- L4 routing also creates a MagicDNS resolution problem (community names aren't visible to personal devices) that ultimately requires a custom DNS layer.
- L7 routing means each community service becomes a normal node on the personal tailnet with a normal MagicDNS name and a normal Let's Encrypt cert. No client-side configuration is needed.

The trade-off — that non-HTTP protocols (SSH, Postgres, raw TCP) are not supported through the bridge — is accepted. Services that need raw TCP can be exposed by other means or wrapped in HTTPS.

### 3.2 Same Canonical Hostname On Both Sides

The community service is configured to think its public name is its **community MagicDNS name** (e.g., `wiki.smithfamily.ts.net`), and the bridge dials that exact name on the community side. The bridge rewrites the response (Host attributes, redirects, cookies) so the personal-side client sees `smithfamily-wiki.<personal-tailnet>.ts.net`.

This means there is a hostname mismatch the bridge must paper over. Existing tsnet reverse-proxy projects (tsbridge, tsnsrv, tsdproxy) do not handle this case because they assume the upstream is a localhost service with no canonical name of its own. The bridge handles it explicitly via Caddy's rewriting primitives (see §8).

### 3.3 Caddy Plus the Tailscale Plugin

The bridge is implemented using Caddy with the official `tailscale/caddy-tailscale` plugin and the official `caddyserver/replace-response` plugin. This gives us, out of the box:

- `bind tailscale/<node>` — Caddy can listen on a tsnet node it spawns directly (no external `tailscaled` needed).
- `transport tailscale <node>` — Caddy can also dial reverse_proxy upstreams through a separate tsnet node it spawns.
- `header_up` / `header_down` — request and response header rewriting with regex support, including Location, Set-Cookie, Host.
- `replace-response` — body content rewriting (substring and regex) for HTML, CSS, and similar.
- `handle_errors` — custom error pages when the upstream is unreachable.

Both directions of the bridge therefore live inside one Caddy process. No fork of Caddy or the plugin is required.

The caddy-tailscale plugin is officially marked experimental. The bridge accepts this risk but isolates blast radius to one user per bridge.

### 3.4 Per-User Bridge Process, User-Owned

Each user runs their own bridge as a single container. The bridge is owned, configured, and operated by the user. There is no central multi-tenant bridge service.

This implies:

- Personal-tailnet auth keys never leave the user's possession.
- Community-tailnet auth keys are provisioned by the community admin and given to the user out-of-band.
- A user joining or leaving a community is a config change on their own bridge, not an action taken by the community.

### 3.5 Hosted Tailscale, MagicDNS

The bridge targets hosted Tailscale (`*.ts.net`) with MagicDNS enabled on both the personal and community tailnets. Headscale support is not in scope for v1.

## 4. Design Principles

These principles are extracted from the design choices above and should guide future decisions about scope, behavior, and additions.

1. **Compose, don't build.** The bridge is glue: a service directory, a configuration generator, and Caddy. When there's a choice between using an existing Caddy/tsnet feature and writing new code, use the existing feature. Custom code is a maintenance burden and a risk.

2. **User sovereignty.** Each user fully owns their bridge. Communities cannot reach into, observe, or control a user's bridge process or personal tailnet. Auth keys flow one way: from community admin to user, not the reverse.

3. **One-way by construction.** The asymmetric access rule is enforced at multiple layers: by Caddy only opening *listeners* on the personal tailnet and only opening *dialers* on the community tailnet, by ACLs on community tailnets that tag bridge nodes as outbound-only, and by the structural fact that the bridge doesn't run any inbound handlers on its community-tailnet nodes. Defense in depth.

4. **Failure is a user-visible event.** When access to a community is lost (key revocation, service down, network issue), the user sees a clear error page identifying what's broken and what to do. The bridge never silently fails or hides degradation.

5. **The directory is authoritative.** The community decides what its services are called, where they live, and what behavior the bridge should apply (e.g., body rewriting). The bridge does not infer or override. Config drift is resolved by re-polling the directory, not by per-user customization.

6. **Hostname-level routing only.** The unit of bridging is a hostname on the personal side ↔ a hostname on the community side. No path-based routing, no per-method routing, no body inspection beyond optional text replacement. This keeps the bridge auditable and predictable.

7. **HTTPS everywhere, no exceptions.** All four legs (client→bridge, bridge→upstream, bridge admin, directory) are HTTPS. WireGuard encryption underneath is not relied upon to allow plaintext above.

8. **Configuration is data, not code.** The bridge is configured by static YAML plus dynamic JSON fetched from each community's directory. No per-user templating logic on the community side, no per-service code on the bridge side.

## 5. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│ Bridge Container (one per user)                                  │
│                                                                  │
│  ┌────────────────────┐         ┌─────────────────────────────┐  │
│  │ orchestrator       │         │ caddy                       │  │
│  │ (Go binary)        │ ───POST→│ (xcaddy-built, admin API)   │  │
│  │                    │ config  │                             │  │
│  │ • reads config.yml │         │ • bind tailscale/<svc>      │  │
│  │ • polls each       │         │   for each community svc    │  │
│  │   community's      │         │   on PERSONAL tailnet       │  │
│  │   directory        │         │                             │  │
│  │ • generates Caddy  │         │ • transport tailscale       │  │
│  │   JSON config      │         │   <community-dialer>        │  │
│  │ • POSTs to Caddy   │         │   for each COMMUNITY        │  │
│  │   admin            │         │                             │  │
│  └────────┬───────────┘         └─────────────────────────────┘  │
│           │                                                      │
│           │ uses tsnet (one node per community, ephemeral)       │
│           │ to dial directory                                    │
│           ▼                                                      │
└──────────────────────────────────────────────────────────────────┘
            │                            │                  │
            │ personal                   │ community A      │ community B
            ▼ (listeners)                ▼ (dialer)         ▼ (dialer)
       personal tailnet            community A tailnet  community B tailnet
```

### 5.1 Components

- **Orchestrator** (custom Go binary). Reads the static bridge config, joins each community tailnet as a small ephemeral tsnet node, fetches the service directory from each community, builds the Caddy JSON config, posts it to Caddy's admin API. Re-polls on a configured interval and on receipt of `SIGHUP`.

- **Caddy** (xcaddy-built binary). Built with `tailscale/caddy-tailscale` and `caddyserver/replace-response` plugins. Runs with its admin API bound to `127.0.0.1:2019`. Spawns one tsnet node per service on the personal side (for listening) and one tsnet node per community (for dialing). Holds all proxy state.

- **Bridge config** (YAML file, user-edited). Lists the user's personal tailnet auth key reference and, for each community they belong to, the directory URL and the community-tailnet auth key reference.

- **Community service directory** (HTTP endpoint, served by the community). JSON document listing services and how the bridge should expose them. Spec in §6.

### 5.2 Data Flow

1. Bridge container starts. Orchestrator reads `config.yml`.
2. For each community: orchestrator starts an ephemeral tsnet node joined to that community using the community auth key. Once the node is online, it fetches `<directory_url>` over HTTPS through that tsnet.
3. Orchestrator merges all directory responses into a single Caddy JSON config and POSTs it to `http://127.0.0.1:2019/load`.
4. Caddy stands up:
   - One tsnet node per (community, service) pair on the personal tailnet, named `<community-prefix><service-name>`, with a tsnet-issued LE cert.
   - One tsnet node per community on the community tailnet, named something like `<personal-tailnet-name>-bridge`, used purely as a dialer.
5. Personal-side clients reach `https://<community-prefix><service-name>.<personal-tailnet>.ts.net`. Caddy terminates TLS, applies header and body rewrites, dials through to `https://<service-name>.<community-tailnet>.ts.net` via the community dialer node.
6. Orchestrator polls each directory on the configured interval (default 5 minutes). If a directory's content has changed (compared by a stable hash of the JSON), orchestrator regenerates and reloads Caddy config. Reload is atomic; in-flight connections are not dropped.
7. On `SIGHUP`, orchestrator polls all directories immediately regardless of interval.

## 6. Community Service Directory — Protocol Specification

The community runs an HTTPS endpoint on its own tailnet that returns a JSON document describing the services available to bridge members. This is the contract between communities and bridges.

### 6.1 Endpoint

- **URL form:** any HTTPS URL on the community tailnet (e.g., `https://directory.smithfamily.ts.net/services`).
- **Method:** `GET`.
- **Auth:** none required at the HTTP layer. Access is gated by tailnet membership (only nodes on the community tailnet can reach it). The directory server may inspect `Tailscale-User-*` whois headers if it wants per-user filtering, but the bridge does not provide them.
- **Response:** `200 OK` with `Content-Type: application/json; charset=utf-8` and a body conforming to §6.2.
- **Caching:** the bridge sends `If-None-Match` with the last seen ETag. The server SHOULD return `304 Not Modified` when content is unchanged. If the server does not implement ETag, the bridge falls back to hashing the response body.

### 6.2 Response Body Schema

```json
{
  "version": 1,
  "community": {
    "name": "Smith Family",
    "tailnet": "smithfamily.ts.net",
    "prefix": "smithfamily-",
    "contact": "admin@smithfamily.example"
  },
  "services": [
    {
      "name": "wiki",
      "upstream_host": "wiki.smithfamily.ts.net",
      "upstream_port": 443,
      "upstream_scheme": "https",
      "description": "Family wiki",
      "rewrite_body": false,
      "rewrite_extra_hosts": []
    },
    {
      "name": "git",
      "upstream_host": "git.smithfamily.ts.net",
      "upstream_port": 443,
      "upstream_scheme": "https",
      "description": "Gitea server",
      "rewrite_body": true,
      "rewrite_extra_hosts": ["git-static.smithfamily.ts.net"]
    }
  ]
}
```

**Fields:**

- `version` (integer, required): protocol version. Currently `1`. Bridges MUST reject directories with an unknown version.
- `community.name` (string, required): human-readable community name, shown in error pages and logs.
- `community.tailnet` (string, required): the community's tailnet name (e.g., `smithfamily.ts.net`). Used for sanity checking upstream hosts; every `upstream_host` MUST be in this domain.
- `community.prefix` (string, required): the hostname prefix used on personal tailnets. MUST match `[a-z0-9]+-` (lowercase alphanumeric plus a trailing hyphen). Examples: `smithfamily-`, `austinmakers-`, `acme-`. The bridge constructs personal-side hostnames as `<prefix><service.name>`.
- `community.contact` (string, optional): displayed in error pages so the user knows who to contact.
- `services` (array, required): the list of services. May be empty.
- `services[].name` (string, required): the service name. MUST match `[a-z0-9][a-z0-9-]*`. Combined with the prefix to form the personal-side hostname. Used directly on the community side: `<name>.<community.tailnet>` is the canonical community-side name.
- `services[].upstream_host` (string, required): the FQDN the bridge dials on the community side. MUST be a subdomain of `community.tailnet`. Typically `<name>.<community.tailnet>` but may differ (e.g., a service that hosts multiple endpoints).
- `services[].upstream_port` (integer, required): the port on the upstream. Typically `443`.
- `services[].upstream_scheme` (string, required): `"https"` (currently the only supported value; `"http"` is reserved and bridges MUST reject it in v1 to enforce TLS everywhere).
- `services[].description` (string, optional): human-readable description.
- `services[].rewrite_body` (boolean, optional, default `false`): if `true`, the bridge applies `replace-response` body rewriting to swap occurrences of `https://<upstream_host>` and `//<upstream_host>` with the personal-side equivalent. Implies `Accept-Encoding: identity` is sent upstream so responses are uncompressed.
- `services[].rewrite_extra_hosts` (array of strings, optional, default `[]`): additional FQDNs to include in body rewriting (e.g., CDN or asset hostnames the service uses). All entries MUST be subdomains of `community.tailnet`. Ignored if `rewrite_body` is `false`.

**Validation rules** (the bridge MUST enforce all of these and reject the entire directory on any violation):

- All `upstream_host` and `rewrite_extra_hosts` values are strict subdomains of `community.tailnet` (no upstreams outside the community tailnet).
- `services[].name` values are unique within the directory.
- `community.prefix` matches the required regex.
- No service name combined with prefix exceeds 63 characters (DNS label limit).

### 6.3 Example Reference Implementation

A minimal reference directory server is out of scope of this spec, but should be a static-file server on the community tailnet that returns the JSON above. ETag support is encouraged but not required.

## 7. Bridge Configuration File

The user maintains a single YAML config file (default path `/etc/bridge/config.yml` or `$BRIDGE_CONFIG`).

```yaml
# Personal tailnet — the one this bridge listens on.
personal:
  # Reference to an auth key. Use _env, _file, or direct value.
  auth_key_env: PERSONAL_TAILNET_AUTHKEY
  # Optional. If omitted, the bridge uses the container hostname.
  # Used only for the orchestrator's housekeeping; per-service listener
  # nodes get their hostnames from the directory.
  bridge_hostname: alice-bridge

# Communities this user belongs to. Zero or more entries.
communities:
  - id: smithfamily              # local identifier; must be unique
    directory_url: https://directory.smithfamily.ts.net/services
    auth_key_env: SMITHFAMILY_AUTHKEY
  - id: austinmakers
    directory_url: https://services.austinmakers.ts.net/directory
    auth_key_env: AUSTINMAKERS_AUTHKEY

# How often to re-poll all directories. Default 5m.
poll_interval: 5m

# How long to wait for a community tsnet node to come online before
# considering it failed. Default 60s.
community_join_timeout: 60s

# State directory. tsnet node state persists here so the bridge keeps
# the same node identity across restarts.
state_dir: /var/lib/bridge

# Caddy admin endpoint. Default is fine inside the container.
caddy_admin_addr: 127.0.0.1:2019
```

**Secret handling:** `auth_key_env`, `auth_key_file`, or `auth_key` (direct) are accepted, matching tsbridge's convention. Direct values are discouraged.

## 8. Caddy Configuration — Generation Rules

The orchestrator generates a Caddy JSON config (not Caddyfile — JSON is what the admin API takes natively and lets us avoid round-tripping). The generation rules are deterministic functions of the bridge config plus the merged directories.

### 8.1 Tailscale App Block

The `tailscale` Caddy app block declares all tsnet nodes Caddy will manage.

For each `(community, service)` pair, declare a personal-side listener node:

```
node id:    "personal-<community.id>-<service.name>"
hostname:   "<community.prefix><service.name>"
auth_key:   <resolved personal auth key>
ephemeral:  false
state_dir:  /var/lib/bridge/personal-<community.id>-<service.name>
```

For each community, declare a community-side dialer node:

```
node id:    "community-dialer-<community.id>"
hostname:   "<personal-tailnet-name>-bridge"
auth_key:   <resolved community auth key>
ephemeral:  false
state_dir:  /var/lib/bridge/community-dialer-<community.id>
```

(The actual property names follow the caddy-tailscale plugin's JSON schema; the orchestrator must map to those exact keys. The structure above is logical.)

### 8.2 Per-Service Site

For each service in each community's directory, emit one Caddy site:

```
# Logical Caddyfile-equivalent for one service. Generated as JSON.
<prefix><service.name>.<personal-tailnet>.ts.net {
  bind tailscale/personal-<community.id>-<service.name>
  tls {
    get_certificate tailscale
  }

  reverse_proxy <upstream_scheme>://<upstream_host>:<upstream_port> {
    transport tailscale community-dialer-<community.id> {
      tls
    }

    # Identity headers (always on)
    header_up X-Tailscale-User        {http.auth.user.tailscale_login}
    header_up X-Tailscale-User-Email  {http.auth.user.tailscale_user}
    header_up X-Tailscale-User-Name   {http.auth.user.tailscale_name}
    header_up X-Tailscale-Node        {http.auth.user.tailscale_node}

    # Forwarding headers. Host is left as the upstream host so the
    # service sees its canonical name.
    header_up Host {upstream_hostport}
    header_up X-Forwarded-Host {host}
    header_up X-Forwarded-Proto https

    # Response rewriting — Location and Set-Cookie always rewritten.
    header_down Location  "https?://<upstream_host>(:443)?"  "https://<prefix><service.name>.<personal-tailnet>.ts.net"
    header_down Set-Cookie "Domain=<upstream_host>;?\s*"     ""

    # When rewrite_body is true:
    header_up Accept-Encoding identity
  }

  # When rewrite_body is true, applied to response body:
  replace {
    "https://<upstream_host>"  "https://<prefix><service.name>.<personal-tailnet>.ts.net"
    "//<upstream_host>"        "//<prefix><service.name>.<personal-tailnet>.ts.net"
    # ... plus one pair per entry in rewrite_extra_hosts.
  }
  encode gzip   # re-compress after body rewriting

  # Error handler — invoked when reverse_proxy fails to reach upstream.
  handle_errors {
    rewrite * /__bridge_error
    reverse_proxy http://127.0.0.1:<orchestrator_error_port>
  }
}
```

**Authentication identity:** the `tailscale_auth` directive is NOT included by default. Personal-side access is already gated by tailnet membership (only the user's own devices can reach their personal tailnet). The `X-Tailscale-User-*` headers come from the listener's whois data and are populated automatically; we just forward them.

### 8.3 Reload Strategy

The orchestrator POSTs the full JSON config to `http://127.0.0.1:2019/load`. Caddy reloads atomically: existing connections drain on the old config while new connections use the new config. If the new config fails to validate, Caddy retains the old config and returns an error; the orchestrator logs the error and retries on the next poll. The orchestrator does not retry rapidly on failure (avoid hammering Caddy with bad config).

### 8.4 Avoided Pitfalls

- **The plugin's "single site connecting to separate Tailscale nodes" warning** in `caddy-tailscale`'s README refers to using `bind tailscale/X` and `transport tailscale Y` on the *same* site with both nodes joining the *same* tailnet. The bridge avoids this by having listeners and dialers on **different** tailnets, which is a clean separation.
- **Hostname mismatch with `Host` header.** We deliberately set `Host` to the upstream hostport because the upstream service believes that's its canonical name. The personal-side hostname is conveyed via `X-Forwarded-Host` for any service that wants to know the original.
- **WebSocket origin checks.** Most apps that check WebSocket origins compare against their configured base URL. Since `Host` going to the upstream is the canonical community name, this works. Apps that take the origin from `X-Forwarded-Host` may need extra handling but are rare.
- **HSTS pinning.** The personal-side `<prefix><service.name>.<personal-tailnet>.ts.net` is a distinct hostname from the community-side one, so HSTS headers from the upstream do not pin the wrong cert.

## 9. Orchestrator — Implementation Plan

The orchestrator is a Go program, single binary, importing `tailscale.com/tsnet` for directory access and the standard library for everything else.

### 9.1 Lifecycle

1. Parse `config.yml`. Validate. Resolve secrets.
2. Start Caddy as a subprocess: `caddy run --config <minimal-bootstrap-config> --resume=false`. The bootstrap config enables only the admin API at `127.0.0.1:2019`.
3. For each community in config:
   a. Create a `tsnet.Server` with the community auth key, `Ephemeral: true`, `Hostname: "<bridge_hostname>-poller-<community.id>"`, `Dir: <state_dir>/poller-<community.id>`.
   b. Wait for the node to reach `Running` state (timeout: `community_join_timeout`).
   c. Use `tsnet.HTTPClient()` to GET the `directory_url`.
   d. Validate the response against §6.2 schema.
4. Merge all directories. Generate Caddy JSON config (§8). POST to admin API.
5. Wait. On every `poll_interval` tick, and on `SIGHUP`, re-fetch all directories.
   - If a directory returns `304 Not Modified` and we have a cached copy, use the cached copy.
   - If a directory returns an error, retain the last good copy and record the error for the `__bridge_error` endpoint.
   - If the merged result differs from the last applied config (by a stable hash), regenerate and POST.
6. On `SIGTERM`/`SIGINT`: send the same signal to the Caddy subprocess, wait up to 30 seconds for it to exit, then exit.

### 9.2 Error Page Endpoint

The orchestrator runs an HTTP server on `127.0.0.1:<orchestrator_error_port>` (default 8081) that responds to `GET /__bridge_error` with a small HTML page. The page is generated from a template and includes:

- Which community the request was destined for (taken from the original `Host` header that Caddy forwards as `X-Forwarded-Host`).
- The community's `name` and `contact` from the most recently fetched directory.
- The most recent polling error for that community, if any (e.g., "Failed to fetch directory: tsnet auth failed", "Last successful fetch: 2 hours ago").
- A suggested action: "Contact the community admin to check your access, or remove this community from your bridge config."

The error page does NOT leak personal-tailnet internals or auth keys. Logs sent to stderr may contain more detail.

### 9.3 Health Tracking Per Community

The orchestrator maintains, for each community:

- `last_successful_poll`: timestamp
- `last_error`: string (empty if last poll succeeded)
- `current_directory`: the last validated directory JSON
- `etag`: the last ETag, if any

These are consulted when rendering the error page and exposed (read-only) on a `/__bridge_status` endpoint on the same port for the user's own debugging.

### 9.4 Caddy Subprocess Management

- Caddy is invoked as a child process with stderr/stdout piped to the orchestrator's stderr/stdout (prefixed with `caddy: `).
- If Caddy exits unexpectedly, the orchestrator exits with a non-zero code. The container runtime (Docker `restart: unless-stopped`, systemd `Restart=on-failure`, etc.) is responsible for restarting the whole bridge.
- The orchestrator does NOT attempt to restart Caddy in-place; that risks state divergence.

### 9.5 Concurrency

- One goroutine per community for polling.
- One goroutine for the error page HTTP server.
- One goroutine for signal handling.
- Caddy config regeneration is serialized: a single mutex around `(read all current directories) → (generate config) → (POST to admin)`.

## 10. Failure Modes and Error Handling

### 10.1 User Removed from Community (Auth Key Revoked)

**Detection paths:**

- The orchestrator's tsnet poller node fails to authenticate or fails to reach the directory. Recorded as `last_error`, displayed on the error page.
- Caddy's `transport tailscale` dialer node for that community also fails to authenticate or to reach the upstream. Reverse_proxy attempts fail and trigger `handle_errors`, which routes to the orchestrator's `/__bridge_error` endpoint.

**User experience:** when the user opens `https://smithfamily-wiki.<personal>.ts.net`, they see the bridge error page explaining the community is unreachable, with the admin's contact info.

**No teardown:** the bridge stays running. The user can remove the community from their `config.yml` and reload (`SIGHUP`) when they're ready.

### 10.2 Community Directory Server Down

Same as 10.1 — orchestrator records the error, error page reflects it. Existing per-service nodes remain registered on the personal tailnet using the last-known good directory. If a service is now removed from the directory but the directory is unreachable, the service node continues to exist (this is acceptable; it'll be cleaned up on the next successful poll).

### 10.3 A Single Service Is Down (but Community Directory Is Up)

The reverse_proxy fails to reach the upstream. `handle_errors` serves the bridge error page, which says (based on the most recent directory's data) "this service appears to be temporarily unreachable." Other services in the community remain available.

### 10.4 Directory Returns Invalid JSON or Fails Schema Validation

The orchestrator rejects the entire response and retains the prior good copy. Error logged. If there's no prior good copy (e.g., on first start), the community is marked as failed and produces error pages for any service the user might try (they don't even know what services exist yet, so they'd be visiting hostnames that don't resolve).

### 10.5 Personal Tailnet Auth Key Invalid

Caddy's listener nodes fail to come online. The bridge fails to listen on anything for that user. Caddy logs the error; the orchestrator passes Caddy stderr through. The container exits and the supervisor restarts it. The user needs to fix their auth key.

### 10.6 Caddy Subprocess Crashes

Orchestrator exits with non-zero status. Supervisor restarts the bridge. tsnet state is persistent (`state_dir`) so the bridge resumes with the same node identities. No re-auth flow is triggered for the user.

### 10.7 Service Name Collision Across Communities

A service called `wiki` in `smithfamily` becomes `smithfamily-wiki`; a service called `wiki` in `austinmakers` becomes `austinmakers-wiki`. They cannot collide as long as community prefixes are distinct. If two communities use the same prefix, the orchestrator detects the collision at directory merge time and refuses to apply the new config; the user must remove one community.

## 11. Build and Deployment

### 11.1 Building Caddy

A `Dockerfile` (or local build script) uses `xcaddy`:

```dockerfile
FROM caddy:builder AS builder
RUN xcaddy build \
    --with github.com/tailscale/caddy-tailscale \
    --with github.com/caddyserver/replace-response

FROM golang:1.22 AS orchestrator
WORKDIR /src
COPY orchestrator/ ./
RUN go build -o /out/orchestrator ./...

FROM debian:stable-slim
COPY --from=builder /usr/bin/caddy /usr/local/bin/caddy
COPY --from=orchestrator /out/orchestrator /usr/local/bin/orchestrator
VOLUME /var/lib/bridge
ENTRYPOINT ["/usr/local/bin/orchestrator"]
```

### 11.2 Container Image Contents

- Single image, two binaries: `caddy` (xcaddy build) and `orchestrator` (custom Go).
- `ENTRYPOINT` is `orchestrator`; it spawns `caddy` as a child.
- One persistent volume mounted at `/var/lib/bridge` for tsnet state across restarts.
- Config file mounted read-only at `/etc/bridge/config.yml` (or passed via `BRIDGE_CONFIG`).
- Auth keys injected via env vars (or files referenced by `_file:` keys in config).
- No exposed ports — all networking is through tsnet. The orchestrator's status server on `127.0.0.1:8081` and Caddy admin on `127.0.0.1:2019` are container-local only.

### 11.3 First-Run Provisioning

1. User obtains a personal-tailnet auth key from their Tailscale admin console.
2. User obtains a community auth key from each community admin out-of-band.
3. User writes `config.yml` listing each community by `id`, `directory_url`, and the env var holding the key.
4. User runs the container with env vars set.
5. Bridge comes up, joins community tailnets, fetches directories, registers personal-side service nodes. Each node going online triggers an LE cert issuance via tsnet (~30s per node, parallelized).
6. User visits `https://<prefix><service>.<personal>.ts.net` from any of their personal devices.

### 11.4 Upgrade

- Bump image tag, restart container. State volume preserves node identities so no re-auth is needed.
- Config changes (adding/removing communities, changing intervals): edit `config.yml`, send `SIGHUP` or restart.

## 12. Out of Scope for v1

These are deliberately excluded; the spec exists to make adding them later a non-disruptive change, not to ship them.

- **Headscale support.** Hosted Tailscale only.
- **Non-HTTPS protocols** (SSH, raw TCP, UDP, Postgres). Use other mechanisms.
- **Path-based service routing** within one hostname. One hostname per service.
- **Per-user authorization at the bridge level.** All authorization happens in the upstream service. The bridge forwards identity headers so the service can decide.
- **Funnel / public exposure.** Bridge is for tailnet-private use only.
- **Bridge-to-bridge federation** or other multi-hop topologies. One bridge, two adjacent tailnets, that's it.
- **Mutating community directories from the bridge side.** The directory is read-only from the bridge's perspective.
- **A web admin UI on the bridge.** CLI / config file only.

## 13. Glossary

- **Personal tailnet:** the user's own Tailscale network, containing only their own devices and the bridge.
- **Community tailnet:** a separate Tailscale network shared among multiple users, containing shared services.
- **Bridge:** the per-user container running orchestrator + Caddy that connects one personal tailnet to one or more community tailnets.
- **Directory:** the JSON document served by a community describing its services.
- **Personal-side hostname:** `<prefix><service-name>.<personal-tailnet>.ts.net` — what the user types in their browser.
- **Community-side hostname / canonical name:** `<service-name>.<community-tailnet>.ts.net` — what the upstream service knows itself as.
- **Prefix:** the community's chosen string (e.g., `smithfamily-`) prepended to all its service names on the personal side.
- **MagicDNS:** Tailscale's automatic DNS for nodes by hostname within a tailnet.
- **tsnet:** Tailscale's Go library for embedding a tailnet node directly in another program.

---

*End of spec.*
