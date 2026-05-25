#!/usr/bin/env bash
# issue-community-cert.sh — issue a wildcard TLS cert for a community subdomain
# via Let's Encrypt + DNS-01.
#
# Run by:    community admin
# Frequency: every ~14 days (rotation = membership cutoff; SPEC §3.5, §6.3)
# Outputs:   <out>/cert.pem and <out>/key.pem
# Implementation: thin wrapper around `lego`. Bash, because that's the
# obvious shape for a tool that shells out to another tool.

set -euo pipefail

usage() {
  cat <<USAGE
Usage: issue-community-cert.sh --domain <community>.ts.<base> \\
                               --provider <lego-dns-provider> \\
                               --email <admin-email> \\
                               --out <dir> \\
                               [--ca <directory-url>] [--staging] [--dry-run]

Issues a wildcard TLS certificate for *.<community>.ts.<base> using Let's
Encrypt (or a compatible ACME CA) with the DNS-01 challenge. The ACME client
is \`lego\`; install with:

  go install github.com/go-acme/lego/v4/cmd/lego@latest

The selected --provider is a lego DNS provider id (e.g. cloudflare, route53,
gandi). lego reads provider-specific credentials from environment variables;
see https://go-acme.github.io/lego/dns/ for the exact env names.

Files produced:
  <out>/cert.pem   — full chain, mode 0644
  <out>/key.pem    — private key, mode 0600

SHA-256 hashes of both files are printed after issuance so the admin can
verify what they distribute. A suggested cron line for the 14-day
rotation cadence is also printed.

Flags:
  --staging    Use Let's Encrypt staging (untrusted certs, no rate limits).
  --ca URL     Override the ACME directory URL (default: LE production).
  --dry-run    Print the lego command this would run, then exit 0.
USAGE
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
    --domain)    DOMAIN="$2"; shift 2;;
    --provider)  PROVIDER="$2"; shift 2;;
    --email)     EMAIL="$2"; shift 2;;
    --out)       OUT="$2"; shift 2;;
    --ca)        CA_URL="$2"; shift 2;;
    --staging)   STAGING=1; shift;;
    --dry-run)   DRY_RUN=1; shift;;
    -h|--help)   usage; exit 0;;
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
require domain   "$DOMAIN"
require provider "$PROVIDER"
require email    "$EMAIL"
require out      "$OUT"

if [ "$STAGING" -eq 1 ]; then
  CA_URL="https://acme-staging-v02.api.letsencrypt.org/directory"
fi

# Shape of DOMAIN: <community>.ts.<basedomain>. The SPEC enforces this on
# the bridge side; we double-check here because issuing a wildcard against
# the wrong shape silently breaks the trust model (SPEC §3.5/§3.6).
case "$DOMAIN" in
  *.ts.*) ;;
  *)
    echo "domain $DOMAIN does not look like <community>.ts.<base>" >&2
    exit 2
    ;;
esac

mkdir -p "$OUT"
LEGO_DATA="$OUT/.lego"

# --- plan -----------------------------------------------------------------

LEGO_CMD=(
  lego
  --accept-tos
  --email "$EMAIL"
  --server "$CA_URL"
  --dns "$PROVIDER"
  --domains "*.${DOMAIN}"
  --path "$LEGO_DATA"
  --key-type ec256
  run
)

if [ "$DRY_RUN" -eq 1 ]; then
  echo "would run:"
  printf '  %q' "${LEGO_CMD[@]}"
  echo
  echo
  echo "  output dir:    $OUT"
  echo "  lego data dir: $LEGO_DATA"
  echo "  result files:  $OUT/cert.pem  $OUT/key.pem"
  exit 0
fi

# --- execute --------------------------------------------------------------

if ! command -v lego >/dev/null 2>&1; then
  cat >&2 <<EOF
error: \`lego\` not on PATH.
install with:
  go install github.com/go-acme/lego/v4/cmd/lego@latest
or download a release from https://github.com/go-acme/lego/releases
EOF
  exit 127
fi

"${LEGO_CMD[@]}"

# lego writes:
#   $LEGO_DATA/certificates/_.<domain>.crt        (full chain)
#   $LEGO_DATA/certificates/_.<domain>.key        (private key)
SRC_CERT="$LEGO_DATA/certificates/_.${DOMAIN}.crt"
SRC_KEY="$LEGO_DATA/certificates/_.${DOMAIN}.key"
if [ ! -f "$SRC_CERT" ] || [ ! -f "$SRC_KEY" ]; then
  echo "error: lego did not produce $SRC_CERT / $SRC_KEY" >&2
  exit 1
fi

install -m 0644 "$SRC_CERT" "$OUT/cert.pem"
install -m 0600 "$SRC_KEY"  "$OUT/key.pem"

echo
echo "wrote:"
echo "  $OUT/cert.pem"
echo "  $OUT/key.pem"
echo
echo "sha256:"
sha256sum "$OUT/cert.pem" "$OUT/key.pem"

if command -v openssl >/dev/null 2>&1; then
  echo
  echo "cert metadata:"
  openssl x509 -in "$OUT/cert.pem" -noout -subject -issuer -dates -ext subjectAltName 2>/dev/null || true
fi

SELF=$(realpath "$0")
OUT_ABS=$(realpath "$OUT")
cat <<EOF

Rotation cadence: every 14 days (SPEC §6.3 — rotation is the membership
cutoff). Suggested cron line:

  0 4 */14 * * $SELF --domain $DOMAIN --provider $PROVIDER --email $EMAIL --out $OUT_ABS

Distribute the resulting cert.pem and key.pem to every current member.
A member who is no longer current MUST NOT receive the new files; their
prior cert expires on its own and locks them out within 14 days.
EOF
