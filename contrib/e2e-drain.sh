#!/usr/bin/env bash
# End-to-end test: server drain HTTP API + server reconciler scheduling.
# Prerequisites:
#   - PostgreSQL reachable (see contrib/dev-postgres.sh)
#   - Database exists, e.g.:
#       createuser kindling 2>/dev/null || true
#       createdb -O kindling kindling_e2e 2>/dev/null || true
#       psql -c "ALTER USER kindling WITH PASSWORD 'kindling';" postgres 2>/dev/null || true
# Usage:
#   KINDLING_E2E_DATABASE_URL=postgres://... ./contrib/e2e-drain.sh
#   # or rely on default kindling_e2e DSN (same as Makefile E2E_DATABASE_URL)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export KINDLING_E2E_DATABASE_URL="${KINDLING_E2E_DATABASE_URL:-postgres://kindling:kindling@127.0.0.1:5432/kindling_e2e?sslmode=disable}"
exec go test -tags=integration -v ./internal/e2e/... -count=1 "$@"
