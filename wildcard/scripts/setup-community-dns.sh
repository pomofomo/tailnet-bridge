#!/usr/bin/env bash
# setup-community-dns.sh — configure Tailscale Split DNS inside a community
# tailnet so that <service>.<community-domain> resolves to the upstream
# service's community-tailnet IP.
#
# Run by:    community admin
# Frequency: once per community (re-run only if the resolver IP changes)
# Implementation: bash + curl against the Tailscale REST API.
#
# Status: STUB. Argument parsing and dry-run are wired; the Tailscale API
# calls are not yet implemented. See SPEC §6.1.

set -euo pipefail

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Configure Split DNS inside a community tailnet for <community-domain>.

This script:
  1. Reads the community tailnet's current DNS settings.
  2. Adds (or replaces) a Split DNS entry mapping <community-domain> to
     <resolver-ip>.
  3. Writes the modified settings back.

The resolver at <resolver-ip> is responsible for answering A queries for
<service>.<community-domain> with the service's community-tailnet IP.
Most communities run a small CoreDNS or Unbound on a community-tailnet
host for this purpose; SPEC §6.1 has a sample config.

Required:
  --community-domain <fqdn>   The community's subdomain (e.g.
                              smithfamily.ts.example.com).
  --tailnet <name>            Community tailnet name (e.g. smithfamily.ts.net,
                              or "-" for the default of the supplied API key).
  --api-key <key>             Tailscale API key (tskey-api-…). MUST be scoped
                              to the community tailnet, not your personal one.
  --resolver-ip <ip[:port]>   Address of the community-side resolver that
                              answers <community-domain> queries.

Optional:
  --dry-run                   Print the API requests that would be made and exit.
  --help                      Show this message.

Tip: read the API key from a file or env var to keep it out of shell history:
  --api-key "\$(cat ~/.tailscale-community-api-key)"
EOF
}

# --- argument parsing -----------------------------------------------------

COMMUNITY_DOMAIN=""
TAILNET=""
API_KEY=""
RESOLVER_IP=""
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --community-domain) COMMUNITY_DOMAIN="$2"; shift 2 ;;
    --tailnet)          TAILNET="$2"; shift 2 ;;
    --api-key)          API_KEY="$2"; shift 2 ;;
    --resolver-ip)      RESOLVER_IP="$2"; shift 2 ;;
    --dry-run)          DRY_RUN=1; shift ;;
    --help|-h)          usage; exit 0 ;;
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
require community-domain "$COMMUNITY_DOMAIN"
require tailnet          "$TAILNET"
require api-key          "$API_KEY"
require resolver-ip      "$RESOLVER_IP"

# Tailscale API: see https://tailscale.com/api
API_BASE="https://api.tailscale.com/api/v2"
NAMESERVERS_URL="$API_BASE/tailnet/$TAILNET/dns/nameservers"

if [ "$DRY_RUN" -eq 1 ]; then
  cat <<EOF
[dry-run] would call:

  GET  $NAMESERVERS_URL
  → read the current "dns" config (split-DNS routes + global nameservers)

  PATCH $NAMESERVERS_URL
  → add (or replace) the split-DNS entry for
        domain   = "$COMMUNITY_DOMAIN"
        resolver = "$RESOLVER_IP"

  Then verify with:
    dig +short @<any-community-tailnet-node> \\
        wiki.$COMMUNITY_DOMAIN
EOF
  exit 0
fi

# --- execute --------------------------------------------------------------
# TODO(impl): implement per Tailscale's DNS API docs.
#   1. curl -H "Authorization: Bearer $API_KEY" "$NAMESERVERS_URL"
#      to fetch the current dns block. (Tailscale uses a single
#      "split DNS" map keyed by domain; we MUST preserve other entries.)
#   2. Merge: set settings.dns.splitDNS["$COMMUNITY_DOMAIN"] = ["$RESOLVER_IP"].
#   3. PATCH/POST back the merged document with appropriate Content-Type.
#   4. On non-2xx, print the response body and exit non-zero.
#   5. Verification step: optionally dig a known service name and confirm
#      it resolves to the expected community-tailnet IP.
#
# As of writing, the relevant API surface is the per-tailnet DNS endpoint;
# confirm the exact request/response shape against the current docs before
# implementing.

echo "error: not yet implemented; pass --dry-run to see the plan" >&2
exit 99
