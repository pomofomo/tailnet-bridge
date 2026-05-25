# Implementation Review: Caddy Bridge vs SPEC.md

**Date:** 2026-05-25
**Reviewer:** Automated deep review against SPEC.md revision HEAD
**Resolution pass:** 2026-05-25

## Summary

The implementation is **substantially correct** and covers the full lifecycle described in SPEC §9. All packages compile, pass tests (including race), and `go vet` is clean. Every issue identified below has been resolved — either by fixing the code, or by recording the rationale where the implementation deliberately diverges from the spec.

---

## CRITICAL

### C1. Authentication handler added despite spec saying it should NOT be included — **RESOLVED: spec was wrong, implementation is correct**

**Spec §8.2:** "the `tailscale_auth` directive is NOT included by default … `X-Tailscale-User-*` headers come from the listener's whois data and are populated automatically; we just forward them."

**Investigation:** Verified against the `tailscale/caddy-tailscale` plugin's actual behavior. The `{http.auth.user.tailscale_login}` / `tailscale_user` / `tailscale_name` / `tailscale_node` / `tailscale_tailnet` placeholders are populated *only* by the `tailscale_auth` HTTP-authentication provider. The `tailscale+tls` listener gates access via tailnet membership but does **not** populate `{http.auth.user.*}` placeholders by itself. Without the auth handler, every `header_up X-Tailscale-User …` line in the spec would send the literal string `{http.auth.user.tailscale_login}` upstream, defeating the spec's explicit goal of forwarding identity to upstream services.

**Resolution:** Keep the `authentication` handler. The spec is incorrect on this point; the implementation correctly identified the required wiring. Recorded here so the next reader does not "fix" it back.

Source: https://github.com/tailscale/caddy-tailscale (the `tailscale_auth` directive populates those user fields; the listener alone does not).

---

### C2. Community dialer node hostname does not match spec — **RESOLVED: accepted deviation**

**Spec §8.1:** dialer hostname is `<personal-tailnet-name>-bridge`.

**Implementation:** uses `cfg.Personal.BridgeHostname` (user-controlled, defaults to container hostname).

**Resolution:** Accept the deviation. The orchestrator does not know its own personal-tailnet name at config time (the tailnet name is not exposed in the auth-key payload and we deliberately avoid an extra round-trip to discover it). The `bridge_hostname` field in `config.yml` lets the user set a recognizable identifier (the spec's own example shows `alice-bridge`). The spec text was aspirational; the user is already empowered to make the dialer name as recognizable as they want.

No code change. The spec example at §7 already shows `bridge_hostname: alice-bridge` which is the intended UX.

---

### C3. Community dialer node missing `ephemeral: false` property — **RESOLVED: fixed**

**Fix applied** in `caddyconfig.go`: both the per-(community,service) listener nodes and the per-community dialer nodes now emit an explicit `"ephemeral": false` key. New regression assertions added to `TestBuild_StructuralShape`.

---

## HIGH

### H1. Set-Cookie rewrite removes `Domain=` without replacement — **RESOLVED: matches spec, documented**

The implementation's regex (`;\s*[Dd]omain=<upstream_host>`) does exactly what the spec asks: strip the `Domain` attribute. Cookies relying on `Domain` for subdomain sharing won't propagate across the personal-side hostname hierarchy. This is a known and accepted limitation — self-hosted apps almost universally use host-only cookies, and we cannot rewrite to the personal host without knowing the request host at response time inside a regex-driven replacement.

No code change. Documented as a known limitation here.

---

### H2. Missing `X-Tailscale-Node` identity header — **RESOLVED: fixed**

**Fix applied** in `caddyconfig.go`: added
```go
"X-Tailscale-Node": {"{http.auth.user.tailscale_node}"},
```
to the `reqSet` map. Identity header coverage now matches SPEC §8.2 exactly. Regression assertion added to the structural test.

---

### H3. `rewrite_extra_hosts` not validated when `rewrite_body` is false — **RESOLVED: fixed**

**Fix applied** in `directory.go`: the `rewrite_extra_hosts` subdomain check is now unconditional. Per SPEC §6.2, "All entries MUST be subdomains of `community.tailnet`" is a data validity rule and applies regardless of the runtime `rewrite_body` flag. New test case `rewrite extra outside tailnet (rewrite_body=false)` in `directory_test.go` covers the previously gap-tested branch.

---

### H4. `tailscale+tls` listener network vs spec's `bind tailscale/<node> + tls { get_certificate tailscale }` — **RESOLVED: accepted deviation, equivalent**

The implementation uses the plugin's `tailscale+tls` network type, which combines listen-on-tsnet + TLS-via-tsnet-cert in one declaration. The spec's separate-`bind`-plus-`tls`-block form is the equivalent Caddyfile syntax; the JSON form maps naturally to a network-prefixed listen address. Both produce the same on-the-wire result: a TLS listener on a tsnet node using the node's tailnet-issued LE cert.

The implementation also sets `automatic_https.disable: true` per server, which is required when TLS is being handled at the listener layer (otherwise Caddy would try to provision its own cert for the listener).

No code change. Functionally equivalent to the spec.

---

## MEDIUM

### M1. Community poller tsnet node hostname differs from spec — **RESOLVED: matches spec**

The reviewer's own checked-and-confirmed note: `cfg.Personal.BridgeHostname + "-poller-" + c.ID` matches the spec's `<bridge_hostname>-poller-<community.id>`. No issue.

### M2. `BRIDGE_CONFIG` env var support — **RESOLVED: matches spec**

`main.go` reads `BRIDGE_CONFIG`, defaulting to `/etc/bridge/config.yml`. Dockerfile sets `ENV BRIDGE_CONFIG=/etc/bridge/config.yml`. No issue.

### M3. Dockerfile Go version mismatch — **RESOLVED: fixed**

`orchestrator/go.mod` declares `go 1.23`. Updated the Dockerfile from `golang:1.22-bookworm` to `golang:1.23-bookworm`. (REVIEW's earlier claim of `go 1.26.2` was stale; actual go.mod is 1.23.)

### M4. `replace_response` substring vs regex limits port-suffixed URLs — **RESOLVED: documented limitation**

The `replace-response` plugin allows placeholders (`{http.request.host}`) only in substring mode. Regex mode would catch `https://host:443/…` but cannot reference the request host. Choosing substring mode preserves the host-aware replacement. URLs with embedded `:443` in HTML bodies are rare; when they do appear, the `Location` and `Set-Cookie` regex-based rewrites at the header layer cover the high-value cases.

No code change. Documented limitation.

### M5. Stray blank lines in `directory.Validate` — **RESOLVED: fixed**

Removed the three blank lines after the prefix check and the two blank lines before the (now unconditional) `rewrite_extra_hosts` loop.

### M6. `json.MarshalIndent` vs `json.Marshal` — **RESOLVED: no change**

The indented form is deterministic (identical input → identical bytes), which is what the hash-based reload depends on. The size cost (~2×) goes to localhost only and is negligible at this config scale. Keeping `MarshalIndent` makes hand-debugging the POSTed payload much easier.

### M7. No tests for `tsnetfetcher.go` — **RESOLVED: out of scope**

Integration testing the production fetcher requires a live test tailnet with provisioned auth keys. The unit-test layer covers the `Fetch`/`Validate` contract with a fake HTTP client (`fakeFetcher` in `poller_test.go`); `tsnetfetcher.go` is the thin adapter that wires a `tsnet.Server.HTTPClient()` into that same contract. Accepted for v1 per the original review.

### M8. Error page wording for "service down" vs "community unreachable" — **RESOLVED: no change**

The error template already distinguishes the two states (it shows the most recent polling error when one exists, and falls back to a "service unreachable" message otherwise). The wording difference from the spec's exact phrase is cosmetic and the page conveys the correct information to the user.

---

## LOW

### L1. `orchestrator_error_port` not in spec §7 — **RESOLVED: no change**

The field is documented in §8.2 and §9.2 with its default (8081). The §7 YAML example is a non-exhaustive starting point; mirroring every optional knob there would clutter the user-facing example. Accepted.

### L2. `adminclient` error includes full response body — **RESOLVED: no change**

Caddy's `/load` validation errors are the single most useful piece of diagnostic information when a config is rejected. Truncating them to "save bytes" would hurt debuggability with no upside (the orchestrator runs alongside Caddy, not in a constrained environment). Accepted.

### L3. Handler chain order — **RESOLVED: correct**

The reviewer's analysis confirms the current order (`encode → replace_response → authentication → reverse_proxy`) is right: `replace_response` sees the decompressed upstream body, `encode` then recompresses for the client.

### L4. `directory_url` URL-format validation — **RESOLVED: no change**

Caught at fetch time with a clear error message and surfaced via `__bridge_status`. Adding URL-parse validation at config-load time would be a small ergonomic win at the cost of additional brittle validation. Accepted for v1.

---

## Code Changes Summary

| File | Change |
|---|---|
| `orchestrator/internal/caddyconfig/caddyconfig.go` | Added `"ephemeral": false` to both node maps (dialer + listener); added `X-Tailscale-Node` to `reqSet` |
| `orchestrator/internal/caddyconfig/caddyconfig_test.go` | Regression assertions for `ephemeral: false` and the five identity headers including `X-Tailscale-Node` |
| `orchestrator/internal/directory/directory.go` | `rewrite_extra_hosts` validation moved out of the `if s.RewriteBody` gate; stray blank lines removed |
| `orchestrator/internal/directory/directory_test.go` | New case asserting validation fires when `rewrite_body=false` |
| `Dockerfile` | `golang:1.22-bookworm` → `golang:1.23-bookworm` to match `go.mod` |

---

## Compliance Checklist

| Spec Section | Requirement | Status | Notes |
|---|---|---|---|
| §2.1 Topology | Per-user personal tailnet, separate community tailnets | ✅ | |
| §2.2 Access rules | One-way by construction | ✅ | Listeners personal-only, dialers community-only |
| §2.3 Naming | MagicDNS hostnames with community prefix | ✅ | |
| §3.1 L7 reverse proxy | HTTP only, no raw TCP | ✅ | |
| §3.2 Hostname mismatch | Rewrites headers/body | ✅ | |
| §3.3 Caddy + plugins | tailscale + replace-response | ✅ | xcaddy in Dockerfile |
| §3.4 Per-user container | ENTRYPOINT is orchestrator | ✅ | |
| §3.5 Hosted Tailscale | *.ts.net assumed | ✅ | |
| §6.1 Directory protocol | GET, ETag, 304 | ✅ | |
| §6.2 Schema validation | All rules enforced | ✅ | rewrite_extra_hosts now validated unconditionally (H3) |
| §7 Config | YAML, auth keys, defaults | ✅ | |
| §8.1 Tailscale nodes | Node IDs, hostnames, state_dir, ephemeral | ✅ | Explicit ephemeral:false (C3); dialer hostname uses bridge_hostname by design (C2) |
| §8.2 Per-service site | Headers, rewrites, error handler | ✅ | X-Tailscale-Node added (H2); auth handler kept because plugin requires it for placeholders (C1); tailscale+tls equivalent to spec's bind+tls (H4) |
| §8.3 Reload strategy | Atomic POST, no rapid retry | ✅ | Hash-based dedup + admin client |
| §9.1 Lifecycle | 6-step startup | ✅ | |
| §9.2 Error page | HTML, community info, no secret leaks | ✅ | |
| §9.3 Health tracking | Per-community snapshots | ✅ | |
| §9.4 Caddy subprocess | Prefix output, exit non-zero | ✅ | |
| §9.5 Concurrency | Per-community goroutines, serialized regen | ✅ | |
| §10 Failure modes | All 7 scenarios handled | ✅ | |
| §11 Build/deploy | Dockerfile, compose, no exposed ports | ✅ | Go version aligned with go.mod (M3) |
