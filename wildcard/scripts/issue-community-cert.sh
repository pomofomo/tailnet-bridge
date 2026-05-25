#!/usr/bin/env bash
# issue-community-cert.sh — issue a wildcard TLS cert for a community subdomain
# via Let's Encrypt + DNS-01.
#
# Run by:    community admin
# Frequency: every ~14 days (rotation = membership cutoff; SPEC §3.5, §6.3)
# Outputs:   <out>/cert.pem and <out>/key.pem
# Implementation: thin wrapper around `lego`. Bash, because that's the
# obvious shape for a tool that shells out to another tool.
#
# Status: STUB. Argument parsing and dry-run are wired; the lego invocation
# is not yet implemented. See SPEC §6.3 for the contract this must satisfy.

set -euo pipefail

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Issue a wildcard TLS cert for *.<community-domain> via Let's Encrypt + DNS-01.

Required:
  --domain <fqdn>          Community subdomain, e.g. smithfamily.ts.example.com
                           The cert will cover *.<fqdn>.
  --provider <name>        lego DNS provider plugin (cloudflare, route53, …)
                           See https://go-acme.github.io/lego/dns/ for the list.
  --email <addr>           ACME account contact email.
  --out <dir>              Output directory. Receives cert.pem and key.pem.

Optional:
  --ca <url>               ACME directory URL.
                           Default: https://acme-v02.api.letsencrypt.org/directory
  --staging                Use Let's Encrypt staging (rate-limit-free; not trusted).
  --dry-run                Print the lego command that would be run, exit 0.
  --help                   Show this message.

Provider credentials are read from environment variables that lego expects
(e.g. CLOUDFLARE_DNS_API_TOKEN, AWS_ACCESS_KEY_ID, …). See the lego docs.

After success, this script prints sha256(cert.pem) and sha256(key.pem) so
you can verify integrity when distributing to members. Suggested
distribution cadence: every 14 days. Suggested cron line is printed at
the end of a successful run.
EOF
}

# --- argument parsing -----------------------------------------------------

DOMAIN=""
PROVIDER=""
EMAIL=""
OUT=""
CA_URL="https://acme-v02.api.letsencrypt.org/directory"
STAGING=0
DRY_RUN=0

while [ $# -gt 0 ]; do
  case "$1" in
    --domain)   DOMAIN="$2"; shift 2 ;;
    --provider) PROVIDER="$2"; shift 2 ;;
    --email)    EMAIL="$2"; shift 2 ;;
    --out)      OUT="$2"; shift 2 ;;
    --ca)       CA_URL="$2"; shift 2 ;;
    --staging)  STAGING=1; shift ;;
    --dry-run)  DRY_RUN=1; shift ;;
    --help|-h)  usage; exit 0 ;;
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
require domain   "$DOMAIN"
require provider "$PROVIDER"
require email    "$EMAIL"
require out      "$OUT"

if [ "$STAGING" -eq 1 ]; then
  CA_URL="https://acme-staging-v02.api.letsencrypt.org/directory"
fi

# --- plan -----------------------------------------------------------------
# This is the command that the real implementation will run. Printing it
# here means --dry-run is useful for review even before the script does
# anything.

LEGO_CMD=(
  lego
    --accept-tos
    --email   "$EMAIL"
    --server  "$CA_URL"
    --dns     "$PROVIDER"
    --domains "*.${DOMAIN}"
    --path    "$OUT"
    run
)

if [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] would execute:"
  printf '  %q' "${LEGO_CMD[@]}"
  echo
  echo "[dry-run] then move issued certificates to:"
  echo "    $OUT/cert.pem"
  echo "    $OUT/key.pem"
  exit 0
fi

# --- execute --------------------------------------------------------------
# TODO(impl): implement per SPEC §6.3.
#   1. mkdir -p "$OUT".
#   2. Verify `lego` is on $PATH; print install hint otherwise.
#   3. Run "${LEGO_CMD[@]}".
#   4. lego writes files under "$OUT"/certificates/_.<DOMAIN>.{crt,key};
#      move/rename them to "$OUT"/{cert,key}.pem.
#   5. chmod 0644 cert.pem and 0600 key.pem.
#   6. Print:
#         sha256(cert.pem)
#         sha256(key.pem)
#         not-before / not-after from `openssl x509 -in cert.pem -dates -noout`
#   7. Print a suggested cron line:
#         0 4 */14 * * /path/to/issue-community-cert.sh --domain ... --out ...
#   8. exit 0.

echo "error: not yet implemented; pass --dry-run to see the plan" >&2
exit 99
