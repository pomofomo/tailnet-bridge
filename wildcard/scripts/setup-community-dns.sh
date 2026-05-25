#!/usr/bin/env bash
# setup-community-dns.sh — configure Tailscale Split DNS inside a community
# tailnet so that <service>.<community-domain> resolves to the upstream
# service's community-tailnet IP.
#
# Run by:    community admin
# Frequency: once per community (re-run only if the resolver IP changes)
# Implementation: bash + curl against the Tailscale REST API.
#
# The Tailscale API exposes split-DNS as a JSON map of domain → list of
# nameserver addresses. PATCH semantics merge keys; we PATCH only the
# community domain so existing entries (other split-DNS zones, magicDNS
# search paths, etc.) are preserved.
#
# See: https://tailscale.com/api#tag/dns

set -euo pipefail

usage() {
  cat <<USAGE
Usage: setup-community-dns.sh --community-domain <community>.ts.<base> \\
                              --tailnet <community-tailnet> \\
                              --api-key <tskey-api-...> \\
                              --resolver-ip <ip[:port]> \\
                              [--dry-run]

Sets the Tailscale Split DNS entry for <community-domain> inside the
community tailnet so that <service>.<community-domain> resolves to
<resolver-ip>. The resolver may be a CoreDNS / Unbound on the tailnet
that returns the community-side service IPs (SPEC §6.1).

The API key must have \`dns:write\` scope. Generate one at
https://login.tailscale.com/admin/settings/keys.

Flags:
  --tailnet <name>    e.g. smithfamily.ts.net (without protocol).
  --resolver-ip <ip>  IP, or IP:port. Bare IP defaults to port 53.
  --dry-run           Print the curl invocations this would run, then exit 0.
USAGE
}

# --- argument parsing -----------------------------------------------------

COMMUNITY_DOMAIN=""
TAILNET=""
API_KEY=""
RESOLVER_IP=""
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --community-domain) COMMUNITY_DOMAIN="$2"; shift 2;;
    --tailnet)          TAILNET="$2"; shift 2;;
    --api-key)          API_KEY="$2"; shift 2;;
    --resolver-ip)      RESOLVER_IP="$2"; shift 2;;
    --dry-run)          DRY_RUN=1; shift;;
    -h|--help)          usage; exit 0;;
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
require community-domain "$COMMUNITY_DOMAIN"
require tailnet          "$TAILNET"
require api-key          "$API_KEY"
require resolver-ip      "$RESOLVER_IP"

# Normalize resolver into "ip:port" form; Tailscale's split-dns map values
# accept either bare ip or ip:port.
case "$RESOLVER_IP" in
  *:*) ;;            # already ip:port (or IPv6 form)
  *)   RESOLVER_IP="${RESOLVER_IP}:53";;
esac

API_BASE="https://api.tailscale.com/api/v2"
SPLIT_URL="$API_BASE/tailnet/$TAILNET/dns/split-dns"

PAYLOAD=$(printf '{"%s":["%s"]}' "$COMMUNITY_DOMAIN" "$RESOLVER_IP")

if [ "$DRY_RUN" -eq 1 ]; then
  echo "would PATCH:"
  echo "  curl -fsS -u '<api-key>:' -X PATCH \\"
  echo "       -H 'Content-Type: application/json' \\"
  echo "       -d '$PAYLOAD' \\"
  echo "       '$SPLIT_URL'"
  echo
  echo "merges {\"$COMMUNITY_DOMAIN\":[\"$RESOLVER_IP\"]} into the existing"
  echo "split-dns map; other domains are preserved."
  exit 0
fi

# --- execute --------------------------------------------------------------

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl not found on PATH" >&2
  exit 127
fi

# Show current state for the admin's reference. Non-fatal on failure
# (some tailnets start with an empty map and the GET returns 200 {} ).
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

Done. Inside the community tailnet, queries for *.${COMMUNITY_DOMAIN}
will now be routed to $RESOLVER_IP.

Verify with (from any node in the tailnet):
  dig +short some-service.${COMMUNITY_DOMAIN}
EOF
