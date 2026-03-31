#!/usr/bin/env bash
# Load-test a URL with k6. Prefers a local k6 binary; falls back to Docker.
#
#   ./contrib/loadtest/run.sh
#   TARGET_URL=https://kindling.systems ./contrib/loadtest/run.sh
#   QUICK=1 ./contrib/loadtest/run.sh
#   MAX_VUS=500 ./contrib/loadtest/run.sh    # smaller spike if needed
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export TARGET_URL="${TARGET_URL:-https://kindling.systems}"

if command -v k6 >/dev/null 2>&1; then
  exec k6 run "${SCRIPT_DIR}/k6-landing.js"
fi

if command -v docker >/dev/null 2>&1; then
  exec docker run --rm -i \
    --ulimit nofile=65535:65535 \
    -v "${SCRIPT_DIR}:/scripts:ro" \
    -e TARGET_URL="${TARGET_URL}" \
    -e QUICK="${QUICK:-}" \
    -e MAX_VUS="${MAX_VUS:-}" \
    grafana/k6:latest run /scripts/k6-landing.js
fi

echo "Install k6 (https://k6.io/docs/get-started/installation/) or Docker." >&2
exit 1
