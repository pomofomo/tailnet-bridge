# REVIEW — Tailnet Community Bridge (wildcard variant)

Scope: implementation in this directory vs. `SPEC.md`. Items are ordered
by severity. Section refs ("SPEC §x.y") point at the spec; file refs at
the implementation.

Legend: ✅ resolved · 📝 acknowledged (no code change needed) · ⏳ open.

## Critical

### C1. ✅ Operator scripts are non-functional stubs — resolved

`scripts/issue-community-cert.sh`, `scripts/setup-community-dns.sh`, and
`scripts/setup-personal-split-dns.sh` now have real implementations:

- `issue-community-cert.sh` shells out to `lego` (DNS-01 via the chosen
  provider), copies the resulting cert+key into the requested output
  directory with the right modes, prints SHA-256 hashes and cert
  metadata via `openssl`, and prints a ready-to-paste 14-day cron line.
  `--dry-run` still prints the planned `lego` invocation without running
  anything.
- `setup-community-dns.sh` and `setup-personal-split-dns.sh` PATCH the
  Tailscale `/api/v2/tailnet/<name>/dns/split-dns` endpoint, preserving
  any other entries in the map. `--dry-run` prints the curl invocation
  it would issue. Both verify `curl` is on PATH and surface non-2xx
  responses verbatim.

`make smoke` continues to exercise `--dry-run` for all three; their
exit code is now `0` when invoked with real arguments and a working
backing tool.

### C2. ✅ Go toolchain mismatch — resolved

`Dockerfile` now uses a single `golang:1.26-bookworm` image for both
the orchestrator build and the xcaddy/caddy build. The `ARG GO_IMAGE`
at the top lets a single bump touch every stage. xcaddy is installed
with `go install` rather than relying on the `caddy:builder` image's
embedded toolchain — so the orchestrator and plugin always compile
against the same Go.

(`tailscale.com v1.98.3` is a real release per the user's check —
left as pinned in `orchestrator/go.mod`.)

### C3. ✅ Cert-failed community has no user-visible error page — resolved

Added `internal/fallbackcert` which generates and persists a self-signed
cert under `state_dir/fallback-{cert,key}.pem` on first start (reused
across restarts; regenerated when expired or tampered with). The
orchestrator passes the resulting paths through `poller.Deps` into
`caddyconfig.Input`.

`caddyconfig.Build` was rewritten so that:

- Every configured community gets a personal-side tsnet listener + a
  bridgedns entry, regardless of cert/directory state.
- Communities with a valid wildcard cert use it (tagged `cert-<id>`);
  communities without fall back to the self-signed cert tagged
  `fallback`. The browser shows a cert warning, and after click-through
  the user reaches `/__bridge_error`, which now renders the friendly
  explanation (SPEC §12.1).
- The community-side dialer tsnet node (`community-dialer-<id>`) is
  ONLY created when a community is "service-OK" (has a real cert AND a
  directory with ≥1 service). This also resolves H2.
- A community is dropped from the output entirely only when it has
  neither a real cert nor a fallback configured — i.e. a programmer
  error, never the runtime cert-failure path.

Updated tests cover the degraded-community path (`Certs` map empty,
`FallbackCertPath` set → server present, routes go to error chain).

### C4. ✅ The 24-hour expiry-warning rule is missing — resolved

`poller.Runner.warnExpiringSoon` is now invoked from both
`reloadAllCerts` and `checkCerts` after a successful validate. It logs
exactly when `cert.MinValidity` (24h) is in play and surfaces the
"expires soon" state on `/__bridge_error` via a new `CertExpiringSoon`
flag that distinguishes it from `CertExpired`. The diagnosis string on
the error page reports "wildcard cert expires soon" before the cert
actually expires.

## High

### H1. ✅ Domain-shape rule duplicated across packages — resolved

Lifted into `internal/dnsutil`. Both `config.validateDomainShape` and
`directory.Validate` now call `dnsutil.ValidateBridgeDomain`, and
`config.baseDomainOf` delegates to `dnsutil.BaseDomain`. Errors and
regexes live in one place.

### H2. ✅ Dialer node spawned even when directory has no services — resolved

See C3 above: `caddyconfig.Build` now only emits
`community-dialer-<id>` when the community has a valid cert AND a
directory with at least one service. Cert-failed or directory-empty
communities still bind the personal listener and bridgedns, but skip
the dialer they would never use.

### H3. 📝 Synchronous tsnet fetcher bring-up — acknowledged, not changed

`NewTsnetFetcher` still serializes startup on the slowest community's
`CommunityJoinTimeout`. The review acknowledged this as acceptable for
the current N=1–3 community count. Revisit when bridges routinely host
more communities.

### H4. ✅ Fragile `caddyExit` channel pattern — resolved

`main.go` now keeps Caddy's exit result in a `caddyErr error` variable
that the wait goroutine writes once before closing a `caddyDone`
channel. Both the main loop and `shutdown` block on `<-caddyDone` and
then read `caddyErr`; no value is "re-injected" into the channel.
`shutdown` is still `sync.Once`-guarded.

### H5. ✅ `make smoke` doesn't actually validate `caddy/bootstrap.json` — resolved

`scripts/smoke.sh` now runs `caddy validate --config
./caddy/bootstrap.json --adapter json` when `caddy` is on PATH; CI
images that ship caddy will fail loudly on a malformed bootstrap.
When the binary is missing (developer laptops without caddy), the
step is skipped with a clear message.

## Medium

### M1. ✅ Misleading SAN error message — resolved

`cert.ErrNoSAN` now wraps the message: `expected wildcard SAN
"*.<domain>" (per-service certs not supported, SPEC §13); got SANs
[…]`. Explicit about what was expected and why.

### M2. ✅ `CertLastReload` surfaced on the error page — resolved

The HTML template's meta footer now renders `Last cert rotation picked
up: <timestamp>` when populated, per SPEC §10.4. The data field is
also exposed verbatim in `/__bridge_status`.

### M3. ✅ Embedded DNS responder accepts deep names — resolved

`bridgedns.handle` now returns NXDOMAIN for any name with two or more
labels below the apex, matching the wildcard cert's coverage and
SPEC §8.2's single-label service-name rule. New test
`TestHandle_DeepLabelNXDomain` covers it.

### M4. ✅ dnscheck logging dedupe — resolved

`dnscheck.Checker` grew an `OnTransition` hook that fires only when a
domain crosses the healthy ↔ violating boundary. `main.go` now logs on
transitions (one line per real change) while `OnResult` keeps the
status snapshot up to date.

### M5. ✅ Chain verification untested — resolved

`cert.Validate` now delegates to a new `cert.ValidateWithRoots`, which
takes an explicit `*x509.CertPool`. New tests
`TestValidateWithRoots_ChainOK` (self-signed leaf used as its own
root) and `TestValidateWithRoots_ChainRejected` (empty pool) exercise
the chain step that previously had no coverage.

### M6. ✅ Comment on `LastError = ""` in the 304 branch — resolved

`pollOnce` now has an explanatory comment in the 304 branch making
the clear-prior-error behaviour intentional and obvious.

### M7. 📝 Cert content hash bakes cert+key into one fingerprint — acknowledged

Behaviour is correct; the note in the original review was a "don't
regress this" reminder, not a defect.

## Low / Style

### L1. ✅ `reloadAllCerts` unused return — resolved

The function no longer returns; its callers (`poller.Start`) used to
discard the bool. The doc comment is updated to match.

### L2. ✅ `caddyproc` pumps could leak — resolved

The pumps now run under a `pumpCtx` plus a goroutine that closes the
underlying pipe on context cancel. `Wait` cancels that context after
the child exits, so the pumps unblock even if the child somehow left
its stdio open.

### L3. ✅ Hard-coded `any.<domain>` canary — resolved

Replaced with a per-`Checker` random label (`bridge-canary-<8hex>`)
generated lazily on first probe. Avoids collision with a community
that legitimately operates an `any` service. `health.RecordDNSResult`
matches the canary by suffix rather than the literal `any.` prefix.

### L4. ✅ `Health.CommunityIDForHost` linear scan — resolved

`health.Store` now keeps a `reverse` map (`domain → community ID`)
maintained inside `SetDomain`. `CommunityIDForHost` and the dnscheck
result matcher consult it instead of walking `domains`. Behaviour is
preserved (apex and one-label-down still match; two-or-more-deep
returns ""), tests are unchanged.

### L5. ✅ `caddy/bootstrap.json` `enforce_origin` override — resolved

Removed. Bootstrap now relies on Caddy's default origin policy
(loopback only), which is what we want.

### L6. 📝 `state_dir` defaults to `/var/lib/bridge` (Linux-only) — acknowledged

No change. The container runs Debian; developer laptops use
`t.TempDir()` in tests or a custom `state_dir` via config override.

### L7. 📝 Auth keys in admin `/load` payload — acknowledged

Inevitable given the caddy-tailscale plugin's JSON shape. The admin
endpoint is loopback-only; bootstrap config doesn't enable request-body
logging. Flagged for vigilance, not a defect to fix.

### L8. 📝 Extra `X-Tailscale-Tailnet` identity header — acknowledged

Implementation extends SPEC §9.2's four-header list to five. Useful,
harmless; documenting rather than removing.

## Verification status

- `go vet`, `gofmt -l`, and `go test ./... -count=1 -race` all clean in
  both modules locally.
- `make smoke` passes: parse + status-server + script `--dry-run` runs.
- `caddy validate` against `caddy/bootstrap.json` runs when `caddy` is on
  PATH (currently skipped on developer machines without it; flagged for
  CI image builds).
- `Dockerfile` build path untested locally (no docker), but the Go stages
  now agree on a single toolchain image (`golang:1.26-bookworm`).

## Quick wins, in order

(Original list — all five are now complete.)

1. ✅ Implement the three `scripts/*.sh` calls.
2. ✅ Reconcile `orchestrator/go.mod` Go directive + Dockerfile.
3. ✅ Wire the 24-hour expiry warning.
4. ✅ Keep the listener + DNS + error route bound for cert-failed
   communities so SPEC §12.1's error-page promise actually holds.
5. ✅ Add `caddy validate` on `caddy/bootstrap.json` in smoke.
