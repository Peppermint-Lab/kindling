#!/usr/bin/env bash
# Load-test a URL with k6. Prefers a local k6 binary; falls back to Docker.
#
#   ./contrib/loadtest/run.sh
#   TARGET_URL=https://kindling.systems ./contrib/loadtest/run.sh
#   PROFILE=smoke ./contrib/loadtest/run.sh
#   PROFILE=medium ./contrib/loadtest/run.sh
#   PROFILE=large ./contrib/loadtest/run.sh
#   PROFILE=viral-4000 ./contrib/loadtest/run.sh
#   QUICK=1 ./contrib/loadtest/run.sh         # backward-compatible smoke run
#   PROFILE=large MAX_VUS=1500 ./contrib/loadtest/run.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export TARGET_URL="${TARGET_URL:-https://kindling.systems}"
export PROFILE="${PROFILE:-}"
export QUICK="${QUICK:-}"
export MAX_VUS="${MAX_VUS:-}"

if command -v k6 >/dev/null 2>&1; then
  exec k6 run "${SCRIPT_DIR}/k6-landing.js"
fi

if command -v docker >/dev/null 2>&1; then
  exec docker run --rm -i \
    --ulimit nofile=65535:65535 \
    -v "${SCRIPT_DIR}:/scripts:ro" \
    -e TARGET_URL="${TARGET_URL}" \
    -e PROFILE="${PROFILE}" \
    -e QUICK="${QUICK}" \
    -e MAX_VUS="${MAX_VUS}" \
    grafana/k6:latest run /scripts/k6-landing.js
fi

echo "Install k6 (https://k6.io/docs/get-started/installation/) or Docker." >&2
exit 1
