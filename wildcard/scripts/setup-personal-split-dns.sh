#!/usr/bin/env bash
# setup-personal-split-dns.sh — configure Tailscale Split DNS on the member's
# personal tailnet so that <community-domain> lookups route to the bridge.
#
# Run by:    member (you)
# Frequency: once per community (re-run only if the bridge IP changes)
# Implementation: bash + curl against the Tailscale REST API.
#
# This is the scripted alternative to the manual flow documented in
# README.md ("Configure personal-tailnet split DNS"). Either path is
# fully supported; this one is here so you don't have to click through
# the admin console N times if you join many communities.
#
# The Tailscale API exposes split-DNS as a JSON map of domain → list of
# nameserver addresses. PATCH semantics merge keys; we PATCH only the
# community domain so existing entries are preserved.
#
# See: https://tailscale.com/api#tag/dns

set -euo pipefail

usage() {
  cat <<USAGE
Usage: setup-personal-split-dns.sh --community-domain <community>.ts.<base> \\
                                   --bridge-tailnet-ip <ip> \\
                                   --api-key <tskey-api-...> \\
                                   [--tailnet <personal-tailnet>] \\
                                   [--port <port>] \\
                                   [--dry-run]

Sets the Tailscale Split DNS entry on the personal tailnet so that
<service>.<community-domain> resolves to the bridge's tailnet IP. The
bridge runs an embedded DNS responder on UDP/<port> (default 53) bound
to its tailnet listener for that community.

The API key must have \`dns:write\` scope.

Flags:
  --tailnet <name>    Defaults to "-" (the API key's default tailnet).
  --port <n>          UDP port the bridge listens on; default 53.
  --dry-run           Print the curl invocations this would run, then exit 0.
USAGE
}

# --- argument parsing -----------------------------------------------------

COMMUNITY_DOMAIN=""
BRIDGE_IP=""
API_KEY=""
TAILNET="-"
PORT=53
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --community-domain)  COMMUNITY_DOMAIN="$2"; shift 2;;
    --bridge-tailnet-ip) BRIDGE_IP="$2"; shift 2;;
    --api-key)           API_KEY="$2"; shift 2;;
    --tailnet)           TAILNET="$2"; shift 2;;
    --port)              PORT="$2"; shift 2;;
    --dry-run)           DRY_RUN=1; shift;;
    -h|--help)           usage; exit 0;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2;;
  esac
done

require() {
  if [ -z "$2" ]; then
    echo "missing required --$1" >&2
    usage >&2
    exit 2
  fi
}
require community-domain  "$COMMUNITY_DOMAIN"
require bridge-tailnet-ip "$BRIDGE_IP"
require api-key           "$API_KEY"

API_BASE="https://api.tailscale.com/api/v2"
SPLIT_URL="$API_BASE/tailnet/$TAILNET/dns/split-dns"
RESOLVER="${BRIDGE_IP}:${PORT}"

PAYLOAD=$(printf '{"%s":["%s"]}' "$COMMUNITY_DOMAIN" "$RESOLVER")

if [ "$DRY_RUN" -eq 1 ]; then
  echo "would PATCH:"
  echo "  curl -fsS -u '<api-key>:' -X PATCH \\"
  echo "       -H 'Content-Type: application/json' \\"
  echo "       -d '$PAYLOAD' \\"
  echo "       '$SPLIT_URL'"
  echo
  echo "merges {\"$COMMUNITY_DOMAIN\":[\"$RESOLVER\"]} into the existing"
  echo "split-dns map; other domains are preserved."
  exit 0
fi

# --- execute --------------------------------------------------------------

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl not found on PATH" >&2
  exit 127
fi

echo "current split-DNS map (before):"
curl -fsS -u "$API_KEY:" "$SPLIT_URL" || echo "  (unable to fetch current state)"
echo

http_body=$(mktemp)
trap 'rm -f "$http_body"' EXIT

http_code=$(curl -sS -u "$API_KEY:" -X PATCH \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD" \
  -o "$http_body" \
  -w "%{http_code}" \
  "$SPLIT_URL")

if [ "$http_code" -lt 200 ] || [ "$http_code" -ge 300 ]; then
  echo "Tailscale API returned HTTP $http_code:" >&2
  cat "$http_body" >&2
  exit 1
fi

echo "OK: PATCH $SPLIT_URL returned HTTP $http_code"
echo

echo "new split-DNS map:"
curl -fsS -u "$API_KEY:" "$SPLIT_URL"
echo

cat <<EOF

Done. From any personal-tailnet device, queries for *.${COMMUNITY_DOMAIN}
will be routed to $RESOLVER (your bridge). The bridge answers DNS only
for hostnames under that exact zone; everything else returns REFUSED.

Verify with:
  dig +short some-service.${COMMUNITY_DOMAIN}
The answer should be your bridge's tailnet IP ($BRIDGE_IP).
EOF
