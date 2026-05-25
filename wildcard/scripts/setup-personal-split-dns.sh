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
# Status: STUB. Argument parsing and dry-run are wired; the Tailscale API
# calls are not yet implemented. See SPEC §7.3.

set -euo pipefail

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Configure Split DNS on YOUR personal tailnet so that lookups for
<community-domain> route to the bridge's per-community listener IP.

This script:
  1. Reads your personal tailnet's current DNS settings.
  2. Adds (or replaces) a Split DNS entry mapping <community-domain>
     to <bridge-tailnet-ip>:53.
  3. Writes the modified settings back.

After this, devices on your personal tailnet that try to resolve
<service>.<community-domain> will ask the bridge, which answers with
its own personal-tailnet IP. The bridge then terminates TLS and
reverse-proxies to the community.

Required:
  --community-domain <fqdn>   The community's subdomain (e.g.
                              smithfamily.ts.example.com).
  --bridge-tailnet-ip <ip>    The bridge's personal-tailnet IP for this
                              community's listener node. After first
                              startup, find it in:
                                docker compose logs bridge
                              or the Tailscale admin console under
                              the bridge node for this community.
  --api-key <key>             Tailscale API key for YOUR personal tailnet,
                              with dns:write scope (tskey-api-…).

Optional:
  --tailnet <name>            Personal tailnet name. Defaults to "-"
                              (the default tailnet of the API key).
  --port <port>               DNS port. Default 53.
  --dry-run                   Print the API requests that would be made,
                              exit 0.
  --help                      Show this message.
EOF
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
    --community-domain)  COMMUNITY_DOMAIN="$2"; shift 2 ;;
    --bridge-tailnet-ip) BRIDGE_IP="$2"; shift 2 ;;
    --api-key)           API_KEY="$2"; shift 2 ;;
    --tailnet)           TAILNET="$2"; shift 2 ;;
    --port)              PORT="$2"; shift 2 ;;
    --dry-run)           DRY_RUN=1; shift ;;
    --help|-h)           usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

require() {
  local name="$1" val="$2"
  if [ -z "$val" ]; then
    echo "error: --$name is required" >&2
    exit 2
  fi
}
require community-domain  "$COMMUNITY_DOMAIN"
require bridge-tailnet-ip "$BRIDGE_IP"
require api-key           "$API_KEY"

API_BASE="https://api.tailscale.com/api/v2"
NAMESERVERS_URL="$API_BASE/tailnet/$TAILNET/dns/nameservers"
RESOLVER="${BRIDGE_IP}:${PORT}"

if [ "$DRY_RUN" -eq 1 ]; then
  cat <<EOF
[dry-run] would call:

  GET  $NAMESERVERS_URL
  → read the current "dns" config (split-DNS routes + global nameservers)

  PATCH $NAMESERVERS_URL
  → add (or replace) the split-DNS entry for
        domain   = "$COMMUNITY_DOMAIN"
        resolver = "$RESOLVER"

  Then verify from a personal-tailnet device:
    dig +short @100.x.y.z wiki.$COMMUNITY_DOMAIN
    # expect: $BRIDGE_IP
EOF
  exit 0
fi

# --- execute --------------------------------------------------------------
# TODO(impl): implement per Tailscale's DNS API docs.
#   1. GET the current dns config.
#   2. Merge: set settings.dns.splitDNS["$COMMUNITY_DOMAIN"] = ["$RESOLVER"].
#      Preserve all other split-DNS entries — never replace the whole map.
#   3. PATCH/POST the merged document back.
#   4. On non-2xx, print the response body and exit non-zero.
#   5. Optional verification: shell out to `dig` against a personal-tailnet
#      resolver and confirm <some-service>.<community-domain> resolves to
#      $BRIDGE_IP.

echo "error: not yet implemented; pass --dry-run to see the plan" >&2
exit 99
