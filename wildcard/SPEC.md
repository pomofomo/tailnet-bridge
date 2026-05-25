# Tailscale Community Bridge (Wildcard Domain) — Specification

## 1. Background

This document specifies an alternative design for the **community bridge**: a per-user process that allows a Tailscale user's personal devices to reach services on one or more shared "community" tailnets, while strictly preventing inbound access from those communities back into the user's personal tailnet.

This variant uses a **community-controlled wildcard domain** (e.g., `*.ts.example.com`) so that the canonical service hostname is identical on both sides of the bridge. The bridge therefore performs no hostname rewriting and works with virtually any HTTPS application, including those that hardcode their canonical name in JavaScript, JSON APIs, or OAuth callbacks.

It is an alternative to the Caddy/MagicDNS bridge specified in `SPEC-CADDY.md`. The two specs share principles but differ substantially in mechanics; this one is meaningfully more capable but requires the community to operate a domain and a cert distribution process.

## 2. Core Requirements

These are unchanged from the earlier bridge design.

### 2.1 Topology

- Each user has a **personal tailnet** containing their own devices and private services.
- A **community tailnet** is a separate tailnet shared among multiple users; it contains services that members of the community can access.
- A user may belong to **zero or more** community tailnets at once. The bridge supports bridging to multiple communities from one personal tailnet.
- Community membership is granted by an admin out-of-band (the admin shares a community-tailnet auth key and a wildcard cert with the user).

### 2.2 Access Rules

The bridge must enforce these access invariants:

- **Allow:** Devices on a personal tailnet can initiate connections to services on any community tailnet the user is a member of.
- **Reject:** Nothing on a community tailnet can initiate a connection to anything on a personal tailnet (no inbound, even for community admins).
- **Reject:** A user's personal devices cannot reach another user's personal tailnet through any path.
- **Reject:** A user's bridge cannot be used by other personal-tailnet members to reach the community.
- **Allow:** Personal-to-personal traffic within a single user's tailnet is unaffected.
- **Allow:** Community-to-community traffic within a single community tailnet is unaffected.

When a user is removed from the community, their personal devices can no longer reach community services. The bridge itself remains running on the personal tailnet but returns a clear error page. The community has no ability to shut down or modify the user's bridge.

### 2.3 Naming and Discovery (Differs From Caddy Spec)

- Each community owns a **dedicated DNS subdomain** under a parent domain it controls. The convention is `<community>.ts.example.com`, where:
  - `example.com` is a domain the operator (likely the same organization that runs the communities, but not necessarily) controls.
  - `ts.` is a fixed prefix under which **only tailnet-routable** hosts ever resolve. Records under `*.ts.example.com` MUST NOT resolve to public-internet IPs. This invariant is critical to the cert-trust model in §3.5.
  - `<community>.` distinguishes one community from another (e.g., `smithfamily.ts.example.com`, `austinmakers.ts.example.com`).
- Services are reached as `https://<service>.<community>.ts.example.com` — the **same name** on both sides of the bridge. The upstream service believes that's its canonical name; the user types the same name into their browser; the bridge does not rewrite anything.
- DNS for `*.<community>.ts.example.com` resolves to the bridge's personal-tailnet IP when queried from a personal tailnet, and to the service's community-tailnet IP when queried from inside the community tailnet. Both are configured via Tailscale split DNS, not public DNS. The public DNS for `*.ts.example.com` MUST return NXDOMAIN or unrouteable addresses; only tailnet-scoped resolution is valid.

## 3. Design Choices (Locked In)

### 3.1 L7 Reverse Proxy, Not L4

Unchanged from the Caddy spec. See `SPEC-CADDY.md` §3.1.

### 3.2 Same Canonical Hostname On Both Sides — Realized

The Caddy spec achieves "same canonical hostname" only in the upstream direction (the upstream sees its real name via `Host`); the client sees a per-user personal-tailnet name and the bridge rewrites Location/Set-Cookie/HTML to paper over the mismatch.

This spec achieves it in both directions: the canonical name `<service>.<community>.ts.example.com` is what the upstream emits, what the client requests, and what flows through the bridge unchanged. No rewriting layer exists. This is the entire reason to take on the domain and cert infrastructure described below.

### 3.3 Caddy Plus the Tailscale Plugin

The bridge is implemented using Caddy with the `tailscale/caddy-tailscale` plugin. The `replace-response` plugin is **not** required for this variant because no body rewriting is needed.

### 3.4 Per-User Bridge Process, User-Owned

Unchanged. See Caddy spec §3.4.

### 3.5 Wildcard Cert Trust Model

A central design choice. Every member of a community holds the same wildcard private key for `*.<community>.ts.example.com` (or, more conservatively, for each individual community subdomain — see §6.4). The community admin generates and distributes this cert.

**Why this is safe:**

- The cert is valid only for names under `*.ts.example.com`.
- The community operator commits, via DNS policy, that no name under `*.ts.example.com` ever resolves on the public internet. (Concretely: the parent zone `ts.example.com` has no public A/AAAA/CNAME records that resolve to internet-routable addresses; only Tailscale split-DNS rules use names in that zone.)
- Therefore, even if a member's wildcard key is compromised, the holder cannot use it to impersonate any service that a victim outside the community could reach over the public internet. They could only impersonate inside a tailnet they already have access to — which is a much weaker capability than wildcard certs normally grant.
- This is the same principle as private CA roots inside corporate networks, but using public-CA infrastructure.

**Operational consequence:** the cert MUST have a short validity (30–60 days suggested), and the community admin MUST be able to revoke a member's access by rotating the cert and not distributing the new one to the removed member. Until rotation, a removed member can still present a valid cert when impersonating bridge services — but combined with Tailscale's access control (the removed member no longer has a community auth key to reach the upstream), this is acceptable.

### 3.6 No Public DNS Under `ts.example.com`

A hard invariant. The parent domain MAY have public records (`www.example.com`, `mail.example.com`); the `ts.` subdomain MUST NOT. This is enforced by the operator at the DNS provider, not by the bridge. The bridge assumes it.

Documentation for community admins must call this out prominently. If `ts.example.com` records leak to public DNS (e.g., through CNAME chains or wildcard misconfiguration), the cert trust model collapses.

### 3.7 Hosted Tailscale

Hosted Tailscale (`*.ts.net`) with the Tailscale DNS feature for split DNS. Headscale not in scope.

## 4. Design Principles

Carried forward from the Caddy spec; one addition specific to this variant.

1. **Compose, don't build.**
2. **User sovereignty.** Each user fully owns their bridge.
3. **One-way by construction.** Asymmetric access enforced at multiple layers.
4. **Failure is a user-visible event.**
5. **The directory is authoritative.**
6. **Hostname-level routing only.**
7. **HTTPS everywhere, no exceptions.**
8. **Configuration is data, not code.**
9. **The `ts.` subdomain is sacred.** No name under `ts.example.com` ever resolves on the public internet. The cert trust model depends on this. Tools and docs must reinforce it; the bridge logs warnings if it can resolve any `ts.example.com` hostname over public DNS.

## 5. Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│ Bridge Container (one per user)                                  │
│                                                                  │
│  ┌────────────────────┐         ┌─────────────────────────────┐  │
│  │ orchestrator       │ ───POST→│ caddy                       │  │
│  │ (Go binary)        │ config  │                             │  │
│  │                    │         │ • bind tailscale/personal   │  │
│  │ • reads config.yml │         │   one node per community    │  │
│  │ • polls each       │         │   (NOT per service)         │  │
│  │   community's      │         │                             │  │
│  │   directory        │         │ • TLS via static wildcard   │  │
│  │ • watches cert     │         │   cert (per community)      │  │
│  │   files for change │         │                             │  │
│  │ • generates Caddy  │         │ • Host-based routing inside │  │
│  │   JSON config      │         │   each community node       │  │
│  └────────┬───────────┘         │                             │  │
│           │                     │ • transport tailscale       │  │
│           │ uses tsnet          │   <dialer> per community    │  │
│           │ (ephemeral) to dial └─────────────────────────────┘  │
│           ▼ directory                                            │
└──────────────────────────────────────────────────────────────────┘
            │                            │                  │
            │ personal                   │ community A      │ community B
            ▼ (one node per community)   ▼ (dialer)         ▼ (dialer)
       personal tailnet            community A tailnet  community B tailnet
```

### 5.1 What's Different From the Caddy Spec

- **One personal-side listener node per community**, not per service. The wildcard cert handles all services under that community's subdomain on a single node. This collapses what would have been many nodes into a small fixed number.
- **No `replace-response` plugin.** No body rewriting.
- **No Location/Set-Cookie header rewriting.** No hostname rewriting at all.
- **Cert material is provisioned out-of-band**, not via tsnet's automatic Let's Encrypt. Caddy uses static cert files.
- **Split DNS is required** on the personal tailnet so that `*.<community>.ts.example.com` resolves to the bridge.

### 5.2 Data Flow

1. Bridge container starts. Orchestrator reads `config.yml`.
2. For each community: orchestrator starts an ephemeral tsnet node joined to that community and fetches the directory.
3. Orchestrator validates the cert files for each community (existence, validity, that the CN/SAN covers `*.<community>.ts.example.com`, expiry > 24h).
4. Orchestrator generates Caddy JSON config and POSTs it to the admin API.
5. Caddy stands up one tsnet listener node per community on the personal tailnet, loads each community's wildcard cert, and starts serving.
6. Personal devices resolve `<service>.<community>.ts.example.com` via Tailscale split DNS to the bridge's personal-tailnet IP for that community. They send the request; Caddy terminates TLS with the wildcard, matches the Host header against the directory, and reverse-proxies to the same hostname on the community side using `transport tailscale`.
7. Orchestrator polls directories on the configured interval. It also re-checks cert files on each tick; if a cert file changes on disk (rotation), Caddy reloads with the new cert.

## 6. The Community Operator's Responsibilities

This variant requires the community admin to set up infrastructure that the Caddy spec does not. This section is the operator's runbook.

### 6.1 DNS Setup

The community operates one DNS subdomain: `<community>.ts.example.com`.

**Requirements:**

- Inside the community tailnet, `<service>.<community>.ts.example.com` resolves to that service's community-tailnet IP. This is done via Tailscale's "Split DNS" feature: configure a nameserver entry in the community tailnet's DNS settings that routes the `<community>.ts.example.com` zone to an internal resolver (most easily, a small CoreDNS or Unbound running on the community tailnet) which returns the right tailnet IPs.
- Inside each member's personal tailnet, `*.<community>.ts.example.com` resolves to that user's bridge's personal-tailnet IP. The user configures this once per community via the Tailscale admin console (see §7.3).
- On the public internet, `<community>.ts.example.com` does NOT resolve. The operator MUST NOT publish A, AAAA, CNAME, or wildcard records for this zone or any subzone.

A sample setup script is provided in `scripts/setup-community-dns.sh` (see §11.3). It uses the Tailscale API to set the community-side split DNS and prints the values the operator should publish to members for their personal-side configuration.

### 6.2 Service Directory

The community publishes the same kind of HTTPS directory as in the Caddy spec, with a slightly different schema (no prefix, no body-rewrite flags). See §8.

### 6.3 Wildcard Cert Generation and Distribution

The community admin generates a wildcard cert covering `*.<community>.ts.example.com` and distributes it to each member.

**Generation:** The admin runs an ACME client (the reference is `lego`, but any DNS-01-capable client works) once, against Let's Encrypt or another public CA. The DNS challenge is satisfied by writing a TXT record to `_acme-challenge.<community>.ts.example.com` via the operator's DNS provider API. The challenge record IS allowed in public DNS — it's only TXT, not an A/AAAA, and doesn't violate the "no public records under ts." rule (which is about *resolving hostnames*, not metadata records).

Two acceptable cert scopes:

- **Per-community wildcard:** one cert valid for `*.<community>.ts.example.com`. Each community has its own wildcard and key. **This is the recommended scope.**
- **Broader wildcard:** one cert valid for `*.ts.example.com` shared across all communities. Simpler operationally, but a key compromise impacts all communities. NOT recommended.

**Validity:** 30 or 60 days. Let's Encrypt's default is 90; shorter validity is enforced by rotating earlier than expiry.

**Rotation cadence:** every 14 days. This means a removed member's stale cert becomes useless within 14 days, regardless of any other action.

**Distribution:** out of band, manual in v1. The admin places the new cert and key in a location each member can retrieve (e.g., a private file share, encrypted email, a secret-management system, a tailnet-only HTTPS endpoint). Each member places the files at the path their bridge expects (see §7.2) and the bridge picks up the change automatically.

A sample admin script `scripts/issue-community-cert.sh` is provided (see §11.3):

```
issue-community-cert.sh \
  --domain smithfamily.ts.example.com \
  --provider cloudflare \
  --email admin@example.com \
  --out ./certs/smithfamily/
```

It runs `lego` with the DNS-01 challenge, writes `cert.pem` and `key.pem` to the output directory, and prints a checksum for verification. In a later version, the admin can replace this with a periodic cron + automated distribution to the tailnet-only HTTPS endpoint.

### 6.4 Member Onboarding

For each new member, the admin performs:

1. Issue a community-tailnet auth key (reusable or single-use per policy).
2. Provide the member with: the auth key, the latest wildcard cert + key, the directory URL, and the values they need for their personal-tailnet split DNS configuration.
3. Subsequently: rotate the wildcard cert on schedule and re-distribute. The admin's distribution list is the source of truth for membership.

### 6.5 Member Offboarding

1. Revoke the member's community-tailnet auth key and remove their node from the community tailnet (Tailscale admin console).
2. Stop distributing the rotated wildcard cert to that member. On the next rotation (within 14 days), the member's bridge starts failing TLS handshakes anyway (cert expired); meanwhile, the lack of a valid community-tailnet membership prevents the bridge from reaching the upstreams.

No action on the member's bridge is required or possible; the community has no access to it.

## 7. Member-Side Configuration

### 7.1 Bridge Config File

```yaml
personal:
  auth_key_env: PERSONAL_TAILNET_AUTHKEY
  bridge_hostname: alice-bridge

communities:
  - id: smithfamily
    domain: smithfamily.ts.example.com    # NEW vs Caddy spec
    directory_url: https://directory.smithfamily.ts.example.com/services
    auth_key_env: SMITHFAMILY_AUTHKEY
    cert_path: /etc/bridge/certs/smithfamily/cert.pem    # NEW
    key_path:  /etc/bridge/certs/smithfamily/key.pem     # NEW

  - id: austinmakers
    domain: austinmakers.ts.example.com
    directory_url: https://directory.austinmakers.ts.example.com/services
    auth_key_env: AUSTINMAKERS_AUTHKEY
    cert_path: /etc/bridge/certs/austinmakers/cert.pem
    key_path:  /etc/bridge/certs/austinmakers/key.pem

poll_interval: 5m
cert_check_interval: 1m
community_join_timeout: 60s
state_dir: /var/lib/bridge
caddy_admin_addr: 127.0.0.1:2019
```

**Differences from the Caddy spec:**

- `domain` is required per community and MUST match what the directory advertises.
- `cert_path` and `key_path` point to PEM files maintained by the user (placed there manually when the admin rotates).
- `cert_check_interval` controls how often the orchestrator stats the cert files for changes.
- No `prefix` field — there is no prefix; canonical names are used directly.

### 7.2 Cert File Layout

Each community has its own subdirectory. The user updates files in place on rotation. The bridge does not modify these files.

```
/etc/bridge/certs/
├── smithfamily/
│   ├── cert.pem
│   └── key.pem
└── austinmakers/
    ├── cert.pem
    └── key.pem
```

File mode SHOULD be `0600` for keys, owned by the user the bridge runs as.

### 7.3 Personal-Tailnet Split DNS

The user configures their personal tailnet to route `<community>.ts.example.com` lookups to a resolver embedded in the bridge — OR, more simply, to a hard-coded IP that is the bridge's tailnet IP.

**Manual setup (Tailscale admin console):**

1. Open the Tailscale admin DNS panel for the personal tailnet.
2. Under "Nameservers," add a "Split DNS" entry:
   - Domain: `smithfamily.ts.example.com`
   - Nameserver: `<bridge-tailnet-ip>:53`
3. Repeat for each community.

The bridge runs a tiny DNS responder on UDP/53 (within the container, on its tailnet-bound listener interface) that answers `*.<community>.ts.example.com` with the corresponding listener node's personal-tailnet IP. This is provided to the user along with the rest of the bridge.

**Scripted setup:** `scripts/setup-personal-split-dns.sh` is provided. It requires a Tailscale OAuth client with `dns:write` scope (separate from the auth key) and performs the steps above via API. The script is optional; the manual flow is fully supported.

### 7.4 First-Run Provisioning Flow

1. User obtains a personal-tailnet auth key from their Tailscale admin console.
2. User receives from the community admin: auth key, wildcard cert + key, directory URL, community domain, community-side contact.
3. User writes `config.yml`, places cert files, sets env vars for the auth keys.
4. User starts the bridge container.
5. User configures personal-tailnet split DNS for each community's domain to point at the bridge (manually or via script).
6. User visits `https://<service>.<community>.ts.example.com` from any personal device.

## 8. Community Service Directory — Protocol Specification

The directory is similar to the Caddy spec's directory but simpler.

### 8.1 Endpoint

Same as Caddy spec §6.1: HTTPS on the community tailnet, GET, no auth at HTTP layer.

### 8.2 Response Body Schema

```json
{
  "version": 1,
  "community": {
    "name": "Smith Family",
    "domain": "smithfamily.ts.example.com",
    "tailnet": "smithfamily.ts.net",
    "contact": "admin@example.com"
  },
  "services": [
    {
      "name": "wiki",
      "upstream_tailnet_host": "wiki.smithfamily.ts.net",
      "upstream_port": 443,
      "description": "Family wiki"
    },
    {
      "name": "git",
      "upstream_tailnet_host": "git.smithfamily.ts.net",
      "upstream_port": 443,
      "description": "Gitea server"
    }
  ]
}
```

**Fields:**

- `version` (integer, required): `1`.
- `community.name` (string, required): human-readable.
- `community.domain` (string, required): the community's subdomain under `ts.example.com`. MUST be exactly two labels deep under a domain ending in `.ts.<...>` — i.e., `<community>.ts.<basedomain>`. The bridge MUST verify this matches the `domain` in its local config; mismatch is a hard error.
- `community.tailnet` (string, required): the community's `*.ts.net` tailnet name. Used only for sanity checking that `upstream_tailnet_host` lives on the expected tailnet.
- `community.contact` (string, optional): displayed on error pages.
- `services[].name` (string, required): matches `[a-z0-9][a-z0-9-]*`. The personal-side hostname is `<name>.<community.domain>` — exactly one level deep under the community domain (no further dots in `name`).
- `services[].upstream_tailnet_host` (string, required): the FQDN the bridge dials on the community side. MUST be a subdomain of `community.tailnet` (the `*.ts.net` name). This is where the bridge actually connects; it is internal plumbing.
- `services[].upstream_port` (integer, required): typically 443.
- `services[].description` (string, optional).

**Notes vs. Caddy spec:**

- No `prefix` field (each community has its own subdomain, no prefix collisions possible).
- No `rewrite_body` or `rewrite_extra_hosts` (no rewriting).
- No `upstream_scheme` (always HTTPS).
- The service's *canonical name* — what its config thinks it is and what the user types — is `<name>.<community.domain>`. The `upstream_tailnet_host` is the routing-layer target; these can differ (e.g., `wiki.smithfamily.ts.example.com` is the canonical name; `wiki.smithfamily.ts.net` is the dial target on the tailnet).

**Validation rules:**

- `services[].name` contains no dots (one level deep).
- `services[].name` values unique within the directory.
- `upstream_tailnet_host` is a subdomain of `community.tailnet`.
- `community.domain` matches the bridge config's local `domain` value for this community.
- Combined `<name>.<community.domain>` is a valid DNS name (≤253 chars, each label ≤63).

## 9. Caddy Configuration — Generation Rules

The orchestrator generates a Caddy JSON config and POSTs it to the admin API. The structure is meaningfully simpler than the Caddy spec because there's no rewriting.

### 9.1 Tailscale App Block

For each community, declare two tsnet nodes:

- Personal-side listener: `personal-<community.id>`, joining the personal tailnet, hostname `<community.id>-bridge` (or similar). One node per community, not per service.
- Community-side dialer: `community-dialer-<community.id>`, joining the community tailnet, hostname `<personal>-bridge`.

### 9.2 Per-Community Site Block (Not Per-Service)

For each community, emit ONE site block that uses the wildcard cert and dispatches all that community's services by Host header.

Logical Caddyfile equivalent:

```
*.<community.domain> {
  bind tailscale/personal-<community.id>

  tls /etc/bridge/certs/<community.id>/cert.pem /etc/bridge/certs/<community.id>/key.pem

  # Match each service by Host. Generated from the directory.
  @wiki host wiki.<community.domain>
  handle @wiki {
    reverse_proxy https://wiki.<community.tailnet>:443 {
      transport tailscale community-dialer-<community.id> { tls }
      header_up X-Tailscale-User       {http.auth.user.tailscale_login}
      header_up X-Tailscale-User-Email {http.auth.user.tailscale_user}
      header_up X-Tailscale-User-Name  {http.auth.user.tailscale_name}
      header_up X-Tailscale-Node       {http.auth.user.tailscale_node}
      # Host is intentionally PRESERVED. Same name on both sides.
    }
  }

  @git host git.<community.domain>
  handle @git {
    reverse_proxy https://git.<community.tailnet>:443 {
      transport tailscale community-dialer-<community.id> { tls }
      # identity headers...
    }
  }

  # Any other host under the wildcard that isn't a known service → error page.
  handle {
    rewrite * /__bridge_error
    reverse_proxy http://127.0.0.1:8081
  }

  handle_errors {
    rewrite * /__bridge_error
    reverse_proxy http://127.0.0.1:8081
  }
}
```

**Key points:**

- One site block per community.
- The wildcard cert is loaded statically from files. No tsnet cert issuance for these names.
- No `header_down Location`, no `header_down Set-Cookie`, no `replace`, no `encode` dance.
- `Host` is preserved upstream (this is Caddy's reverse_proxy default when the upstream is a hostname — but we set it explicitly for clarity).
- An unknown subdomain under the wildcard hits the catch-all and returns a bridge error page (helpful for typos and removed services).

### 9.3 Cert File Reload

Caddy supports static cert files. To reload after rotation, the orchestrator POSTs a fresh config with the same cert paths; Caddy re-reads the files. The orchestrator detects rotation by polling file `mtime` and content hash on `cert_check_interval` (default 1m). If hash changed, regenerate config.

### 9.4 Cert Validation

Before applying a config that references a cert file, the orchestrator:

1. Parses the PEM and verifies a private key matches.
2. Verifies at least one SAN matches `*.<community.domain>` (or the exact set of service hostnames, if the cert is non-wildcard).
3. Verifies expiry is more than 24 hours away. If less, log a warning but apply anyway. If already expired, refuse to apply and keep prior config.
4. Verifies the cert chain validates against the system trust store (Let's Encrypt is a public CA, so this works).

If validation fails, the orchestrator skips this community in the new config (the rest of the bridge keeps working) and surfaces the error on the error page.

### 9.5 Sanity Check on Startup

Once at startup and once per `poll_interval`, the orchestrator resolves `<community.domain>` over public DNS (using the host's resolver bypassing tailnet split DNS — i.e., querying `8.8.8.8` or similar directly). If it gets back any A/AAAA record, log a loud warning: the trust model has been violated. The bridge still runs (it can't enforce DNS policy on a third party's domain), but this is critical signal to the operator.

## 10. Orchestrator — Implementation Plan

Largely the same as the Caddy spec §9, with these differences:

### 10.1 New Responsibilities

- **Cert file watcher.** A goroutine polls all `cert_path`/`key_path` pairs every `cert_check_interval`. On change (mtime + content hash), trigger a config regeneration.
- **Cert validation.** As in §9.4.
- **DNS sanity check.** Background goroutine, every `poll_interval`, resolves community domains over the public resolver and warns on success.
- **Embedded DNS responder.** A small DNS server bound to UDP/53 on the bridge's personal-tailnet IP (per community node), responding to `*.<community.domain>` queries with that node's tailnet IP. The personal tailnet's Tailscale split DNS is configured to route those queries here.

### 10.2 No Longer Required

- No per-service node creation.
- No per-service Let's Encrypt issuance.

### 10.3 Lifecycle

1. Parse config.
2. Validate all cert/key files exist and are well-formed.
3. Start Caddy subprocess.
4. For each community: spawn ephemeral tsnet poller, fetch directory, validate, store.
5. Generate full Caddy config, POST to admin.
6. Steady state: poll directories, watch cert files, react to changes.
7. On `SIGHUP`: immediate re-poll of everything.
8. On `SIGTERM`: shut down Caddy, exit.

### 10.4 Error Page Endpoint

Same as Caddy spec §9.2. Page now also reports cert expiry per community and last cert rotation timestamp. Helps users know when to nag the admin.

## 11. Build and Deployment

### 11.1 Building Caddy

```dockerfile
FROM caddy:builder AS builder
RUN xcaddy build \
    --with github.com/tailscale/caddy-tailscale
# replace-response NOT needed in this variant

FROM golang:1.22 AS orchestrator
WORKDIR /src
COPY orchestrator/ ./
RUN go build -o /out/orchestrator ./...

FROM debian:stable-slim
COPY --from=builder /usr/bin/caddy /usr/local/bin/caddy
COPY --from=orchestrator /out/orchestrator /usr/local/bin/orchestrator
VOLUME /var/lib/bridge
VOLUME /etc/bridge/certs
ENTRYPOINT ["/usr/local/bin/orchestrator"]
```

### 11.2 Volumes

- `/var/lib/bridge` — tsnet state, persistent across restarts.
- `/etc/bridge/certs` — wildcard cert files, mounted from a directory the user updates on rotation.
- `/etc/bridge/config.yml` — config file, read-only.

### 11.3 Sample Scripts (Documented but Optional)

The repository should provide these scripts under `scripts/`. They are operator/user conveniences, not part of the bridge runtime.

**`scripts/issue-community-cert.sh`** (run by community admin):

- Wraps `lego` with DNS-01 against the operator's DNS provider.
- Args: `--domain`, `--provider`, `--email`, `--out`.
- Produces `cert.pem` and `key.pem`; prints SHA-256 of both for verification.
- Documents the suggested 14-day rotation cadence and the cron line to add.

**`scripts/setup-community-dns.sh`** (run by community admin):

- Uses the Tailscale API to set up split DNS inside the community tailnet so `<community>.ts.example.com` resolves to community-side service IPs.
- Args: `--community-domain`, `--tailnet`, `--api-key`, `--resolver-ip`.
- Documents how to alternatively run a small CoreDNS on the community tailnet and point Tailscale Split DNS at it.

**`scripts/setup-personal-split-dns.sh`** (run by member):

- Uses the Tailscale API to configure split DNS on the personal tailnet so `<community>.ts.example.com` resolves to the local bridge.
- Args: `--community-domain`, `--bridge-tailnet-ip`, `--api-key`.
- Documents the alternative manual flow via the admin console.

Each script has a `--dry-run` mode that prints the actions it would take.

### 11.4 Upgrade

Bump image tag, restart container. tsnet state and cert files persist; no re-auth needed.

## 12. Failure Modes and Error Handling

Most modes are the same as the Caddy spec §10. Differences and additions:

### 12.1 Cert Expired or Missing

The orchestrator detects this during validation. Affected community is skipped (other communities keep working). Error page for `<service>.<that-community>.ts.example.com` explains: "Bridge cert for this community has expired (or is missing). Contact the community admin: <contact>."

### 12.2 Cert Domain Mismatch

If the cert's SANs do not cover the configured `domain`, validation fails as in §12.1.

### 12.3 Cert Rotation Picked Up Mid-Request

Caddy's reload is atomic. In-flight requests use the old cert; new requests use the new one. No client-visible interruption beyond the brief reload moment.

### 12.4 Public DNS Resolution of `*.ts.example.com` Detected

Logged loudly, surfaced on `/__bridge_status`. Bridge continues to operate. This is a serious operator-side warning, not a user error.

### 12.5 Split DNS Misconfigured on Personal Tailnet

Personal devices fail to resolve `<service>.<community>.ts.example.com` at all (NXDOMAIN). Bridge never sees the request. User troubleshoots by checking their Tailscale admin DNS settings; documentation should call this out as the first thing to verify when "nothing works."

### 12.6 Member Removed From Community

Two layers of failure, both producing reasonable behavior:

- Within 14 days: the community-tailnet auth key gets revoked. `transport tailscale` dial fails. `handle_errors` returns the bridge error page.
- After next rotation: the member's wildcard cert expires. TLS handshake fails. Browser shows a cert error. (The orchestrator also detects the impending expiry and shows it on the status page.)

Either way, the member loses access cleanly. The user can remove the community from `config.yml`.

## 13. Out of Scope for v1

Same as the Caddy spec §12. Additional v1 exclusions specific to this variant:

- **Automatic cert distribution.** Admin manually distributes; future versions may add a pull mechanism from a tailnet-internal endpoint.
- **Per-service certs.** Only wildcard certs supported.
- **Cert pinning or revocation lists.** Standard public-CA trust only.
- **Multiple community-domain roots.** All communities live under the same `ts.<basedomain>` parent. Mixing `ts.example.com` with `ts.example.org` in one bridge is not supported.

## 14. Glossary

In addition to the Caddy spec's glossary:

- **Wildcard domain:** `*.<community>.ts.example.com`, the DNS subdomain for one community.
- **Base domain:** the domain the operator controls (`example.com`).
- **Tailnet-only subdomain:** `ts.example.com` and everything under it — names that MUST NOT resolve on the public internet.
- **Canonical name:** `<service>.<community>.ts.example.com` — what both the upstream and the user use. Identical on both sides of the bridge.
- **Wildcard cert:** the public-CA-issued cert held by every member of a community, covering `*.<community>.ts.example.com`.
- **Cert rotation:** the periodic (e.g., 14-day) reissuance and redistribution of the wildcard cert. The membership cutoff mechanism.

---

*End of spec.*
