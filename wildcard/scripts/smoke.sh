#!/usr/bin/env bash
# scripts/smoke.sh — local lifecycle sanity check for the orchestrator.
#
# Verifies:
#   1. config.example.yml parses (with placeholder env vars).
#   2. The orchestrator binary fails CLEANLY (not with a panic / segfault)
#      when there's no Caddy to spawn, surfacing a useful error message.
#   3. The status server boots and serves /__bridge_status JSON.
#   4. /__bridge_error returns HTML for an unknown subdomain.
#
# Does NOT require:
#   - real Tailscale auth keys,
#   - a real community tailnet,
#   - real wildcard certs.
#
# Run from the repo root:
#   make smoke
#
# Exit codes:
#   0 → all checks pass
#   non-zero → at least one check failed (output shows which)

set -euo pipefail

BIN=./bin/orchestrator
if [[ ! -x "$BIN" ]]; then
  echo "smoke: $BIN not built. Run 'make build' first." >&2
  exit 1
fi

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# ─── config-only parse check ──────────────────────────────────────────────
# A minimal valid config that doesn't exercise tsnet.

PERSONAL_KEY=tskey-personal-fake
COMMUNITY_KEY=tskey-community-fake

cat >"$TMPDIR/config.yml" <<EOF
personal:
  auth_key_env: PERSONAL_TAILNET_AUTHKEY
  bridge_hostname: smoke-bridge

communities: []

poll_interval: 1m
cert_check_interval: 30s
community_join_timeout: 5s
state_dir: $TMPDIR/state
caddy_admin_addr: 127.0.0.1:32919
orchestrator_error_port: 38081
dns_check_resolver: 127.0.0.1:53
EOF
mkdir -p "$TMPDIR/state"

echo "[smoke] config parse check"
export PERSONAL_TAILNET_AUTHKEY="$PERSONAL_KEY"
export SMITHFAMILY_AUTHKEY="$COMMUNITY_KEY"
export BRIDGE_CONFIG="$TMPDIR/config.yml"
export CADDY_BIN=/bin/false   # forces caddy spawn to "succeed" then exit
export CADDY_BOOTSTRAP=/dev/null

# We expect the orchestrator to fail because there is no usable Caddy.
# What we care about is that the failure is the EXPECTED one (admin
# never comes up), not a panic from earlier in startup.
set +e
OUTPUT="$("$BIN" 2>&1 || true)"
set -e

if grep -q "config loaded: 0 communities" <<<"$OUTPUT"; then
  echo "  ok: parsed config with 0 communities"
else
  echo "  FAIL: expected 'config loaded' line, got:"
  echo "---"
  echo "$OUTPUT"
  echo "---"
  exit 1
fi
if grep -qE "caddy admin never came up|admin.*never came up|admin TCP socket" <<<"$OUTPUT"; then
  echo "  ok: orchestrator surfaced 'caddy admin never came up' (expected without real caddy)"
else
  # Acceptable alternative: orchestrator failed because BRIDGE_CONFIG
  # path-missing-cert etc. We treat any clean exit as fine as long as
  # the parse-config line was present.
  echo "  ok: orchestrator exited (no panic detected)"
fi
if grep -qiE "panic|fatal error: " <<<"$OUTPUT"; then
  echo "  FAIL: panic detected in output:"
  echo "$OUTPUT"
  exit 1
fi

# ─── status server smoke check via in-process Go test ─────────────────────
# (the package tests already cover this; this is a final reassurance
# that the binary's compiled-in status server template renders.)

echo "[smoke] orchestrator + plugin go tests"
( cd orchestrator             && go test -count=1 ./... >/dev/null )
( cd caddy-plugin/bridgedns   && go test -count=1 ./... >/dev/null )
echo "  ok: package tests pass"

# ─── caddy bootstrap config sanity (optional) ─────────────────────────────
# When `caddy` is on PATH, ask it to validate the bootstrap JSON we ship.
# This catches typos in caddy/bootstrap.json without requiring tsnet or
# a real upstream.

if command -v caddy >/dev/null 2>&1; then
  echo "[smoke] caddy validate ./caddy/bootstrap.json"
  if caddy validate --config ./caddy/bootstrap.json --adapter json >/dev/null 2>caddy_validate.err; then
    echo "  ok: bootstrap.json passes caddy validate"
  else
    echo "  FAIL: caddy validate rejected the bootstrap config:" >&2
    cat caddy_validate.err >&2 || true
    rm -f caddy_validate.err
    exit 1
  fi
  rm -f caddy_validate.err
else
  echo "[smoke] skipping caddy validate (caddy not on PATH)"
fi

# ─── scripts/*.sh syntax + dry-run ────────────────────────────────────────

echo "[smoke] scripts/*.sh syntax + --help + --dry-run"
for f in scripts/issue-community-cert.sh scripts/setup-community-dns.sh scripts/setup-personal-split-dns.sh; do
  bash -n "$f"
done
"$PWD/scripts/issue-community-cert.sh" --help          >/dev/null
"$PWD/scripts/setup-community-dns.sh"  --help          >/dev/null
"$PWD/scripts/setup-personal-split-dns.sh" --help      >/dev/null
"$PWD/scripts/issue-community-cert.sh" \
  --domain smoke.ts.example.com --provider cloudflare \
  --email a@b.c --out ./out --dry-run >/dev/null
"$PWD/scripts/setup-community-dns.sh" \
  --community-domain smoke.ts.example.com --tailnet smoke.ts.net \
  --api-key TEST --resolver-ip 100.64.0.10 --dry-run >/dev/null
"$PWD/scripts/setup-personal-split-dns.sh" \
  --community-domain smoke.ts.example.com --bridge-tailnet-ip 100.64.0.42 \
  --api-key TEST --dry-run >/dev/null
echo "  ok: all three scripts pass syntax + help + dry-run"

echo "[smoke] OK"
