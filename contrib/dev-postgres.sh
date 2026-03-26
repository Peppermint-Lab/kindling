#!/usr/bin/env bash
# Optional local PostgreSQL for Kindling (replaces docker compose postgres).
# Usage: contrib/dev-postgres.sh start|stop|status
# Expects PostgreSQL 15+ installed (e.g. brew install postgresql@17) and a DB matching DATABASE_URL in the Makefile.
set -euo pipefail

ACTION="${1:-start}"

if [ "$ACTION" = "start" ] && pg_isready -h 127.0.0.1 -p 5432 &>/dev/null; then
  echo "Postgres already listening on 5432"
  exit 0
fi

find_pg_ctl() {
  if command -v pg_ctl &>/dev/null; then
    dirname "$(command -v pg_ctl)"
    return 0
  fi
  for d in \
    /opt/homebrew/opt/postgresql@17/bin \
    /opt/homebrew/opt/postgresql@16/bin \
    /usr/local/opt/postgresql@17/bin \
    /usr/local/opt/postgresql@16/bin; do
    if [ -x "$d/pg_ctl" ]; then
      echo "$d"
      return 0
    fi
  done
  return 1
}

BIN_DIR=$(find_pg_ctl || true)
if [ -z "${BIN_DIR:-}" ]; then
  echo "PostgreSQL dev tools not found. Install one of:" >&2
  echo "  macOS: brew install postgresql@17" >&2
  echo "  Linux: sudo apt install postgresql (then create user/db — see contrib/remote/provision.sh)" >&2
  exit 1
fi

export PATH="$BIN_DIR:$PATH"
PGDATA="${KINDLING_PGDATA:-$HOME/.kindling/pgdata}"

case "$ACTION" in
  start)
    if [ ! -d "$PGDATA/base" ]; then
      echo "Initializing cluster at $PGDATA ..."
      initdb -D "$PGDATA" --locale=C -U "$USER" >/dev/null
      cat >> "$PGDATA/postgresql.conf" <<'EOF'
wal_level = logical
max_replication_slots = 10
max_wal_senders = 10
EOF
    fi
    if pg_ctl -D "$PGDATA" status &>/dev/null; then
      echo "Cluster already running"
    else
      pg_ctl -D "$PGDATA" -l "$PGDATA/logfile" start
    fi
    echo "Ensure role/database exist (once), e.g.:"
    echo "  createuser kindling  &&  createdb -O kindling kindling"
    echo "  psql -c \"ALTER USER kindling WITH PASSWORD 'kindling';\""
    ;;
  stop)
    pg_ctl -D "$PGDATA" stop || true
    ;;
  status)
    pg_ctl -D "$PGDATA" status || true
    ;;
  *)
    echo "usage: $0 start|stop|status" >&2
    exit 1
    ;;
esac
