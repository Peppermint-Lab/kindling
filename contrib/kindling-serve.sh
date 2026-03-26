#!/usr/bin/env bash
# Production entrypoint for systemd. Installed to /usr/local/lib/kindling/serve.sh.
set -euo pipefail

ENV_FILE=/etc/kindling/kindling.env
if [[ ! -f "$ENV_FILE" ]]; then
  echo "kindling: missing $ENV_FILE (see contrib/kindling-prod.env.example)" >&2
  exit 1
fi

set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

: "${KINDLING_HOME:?set KINDLING_HOME in $ENV_FILE}"
: "${KINDLING_ADVERTISE_HOST:?set KINDLING_ADVERTISE_HOST in $ENV_FILE}"
: "${KINDLING_PUBLIC_URL:?set KINDLING_PUBLIC_URL in $ENV_FILE}"
: "${KINDLING_ACME_EMAIL:?set KINDLING_ACME_EMAIL in $ENV_FILE}"

KINDLING_LISTEN="${KINDLING_LISTEN:-:8080}"
KINDLING_EDGE_HTTP="${KINDLING_EDGE_HTTP:-:80}"
KINDLING_EDGE_HTTPS="${KINDLING_EDGE_HTTPS:-:443}"

STAGING_FLAG=()
if [[ "${KINDLING_ACME_STAGING:-}" == "1" || "${KINDLING_ACME_STAGING:-}" == "true" ]]; then
  STAGING_FLAG=(--acme-staging)
fi

cd "$KINDLING_HOME"
exec "$KINDLING_HOME/bin/kindling" serve \
  --listen "$KINDLING_LISTEN" \
  --advertise-host "$KINDLING_ADVERTISE_HOST" \
  --public-url "$KINDLING_PUBLIC_URL" \
  --edge-http "$KINDLING_EDGE_HTTP" \
  --edge-https "$KINDLING_EDGE_HTTPS" \
  --acme-email "$KINDLING_ACME_EMAIL" \
  "${STAGING_FLAG[@]}"
