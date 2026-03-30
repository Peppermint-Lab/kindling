#!/usr/bin/env bash
set -euo pipefail

# Host a native PostgreSQL dev instance inside the persistent kindling-mac box VM.
# Usage: contrib/dev-postgres-vm.sh init|start|stop|status

ACTION="${1:-start}"
DB_NAME="${KINDLING_VM_POSTGRES_DB:-kindling}"
DB_USER="${KINDLING_VM_POSTGRES_USER:-kindling}"
DB_PASSWORD="${KINDLING_VM_POSTGRES_PASSWORD:-kindling}"
PGDATA="${KINDLING_VM_POSTGRES_DATA:-/var/lib/postgresql/data}"
PGPORT="${KINDLING_VM_POSTGRES_PORT:-5432}"
PGSOCKET_DIR="${KINDLING_VM_POSTGRES_SOCKET_DIR:-/tmp}"
HOST_DSN="postgres://${DB_USER}:${DB_PASSWORD}@127.0.0.1:${PGPORT}/${DB_NAME}?sslmode=disable"

usage() {
  cat <<EOF
Usage: $(basename "$0") init|start|stop|status

Environment overrides:
  KINDLING_VM_POSTGRES_DB
  KINDLING_VM_POSTGRES_USER
  KINDLING_VM_POSTGRES_PASSWORD
  KINDLING_VM_POSTGRES_DATA
  KINDLING_VM_POSTGRES_PORT
  KINDLING_VM_POSTGRES_SOCKET_DIR
EOF
}

ensure_kindling() {
  command -v kindling >/dev/null 2>&1 || {
    echo "missing required command: kindling" >&2
    exit 1
  }
}

ensure_box_running() {
  kindling local box start >/dev/null
}

run_box_sh() {
  local script="$1"
  kindling local box exec --cwd / -- sh -ceu "$script"
}

ensure_guest_network() {
  run_box_sh "$(cat <<'EOF'
set -eu
ip link set lo up || true
ip link set eth0 up || true
ip addr flush dev eth0 || true
ip addr add 192.168.64.2/24 dev eth0 || true
ip route del default 2>/dev/null || true
ip route add default via 192.168.64.1 || true
mkdir -p /etc
cat >/etc/resolv.conf <<RESOLV
nameserver 1.1.1.1
nameserver 8.8.8.8
RESOLV
EOF
)"
}

install_postgres() {
  ensure_guest_network
  run_box_sh "$(cat <<'EOF'
set -eu
if command -v pg_ctl >/dev/null 2>&1 && command -v psql >/dev/null 2>&1; then
  exit 0
fi
apk update >/dev/null
if apk add --no-cache postgresql17 postgresql17-client >/dev/null 2>&1; then
  exit 0
fi
apk add --no-cache postgresql postgresql-client >/dev/null
EOF
)"
}

ensure_initialized() {
  install_postgres

  run_box_sh "$(cat <<EOF
set -eu
PGDATA="${PGDATA}"
PGPORT="${PGPORT}"
DB_NAME="${DB_NAME}"
DB_USER="${DB_USER}"
DB_PASSWORD="${DB_PASSWORD}"

mkdir -p "\$(dirname "\$PGDATA")" "\$PGDATA"
if ! id postgres >/dev/null 2>&1; then
  adduser -D -h /var/lib/postgresql -s /bin/sh postgres >/dev/null 2>&1 || true
fi
chown -R postgres:postgres "\$(dirname "\$PGDATA")" "\$PGDATA" >/dev/null 2>&1 || chown -R postgres "\$(dirname "\$PGDATA")" "\$PGDATA" >/dev/null 2>&1 || true

if [ ! -f "\$PGDATA/PG_VERSION" ]; then
  su postgres -c "initdb -D '\$PGDATA'" >/tmp/kindling-postgres-init.log
fi

if ! grep -q "kindling dev postgres settings" "\$PGDATA/postgresql.conf"; then
  cat >>"\$PGDATA/postgresql.conf" <<POSTGRES_CONF

# kindling dev postgres settings
listen_addresses = '127.0.0.1'
port = \$PGPORT
POSTGRES_CONF
fi

if ! grep -q "kindling dev postgres access" "\$PGDATA/pg_hba.conf"; then
  cat >>"\$PGDATA/pg_hba.conf" <<PG_HBA

# kindling dev postgres access
host all all 127.0.0.1/32 scram-sha-256
PG_HBA
fi
EOF
)"
}

ensure_started() {
  ensure_initialized

  run_box_sh "$(cat <<EOF
set -eu
PGDATA="${PGDATA}"
PGPORT="${PGPORT}"
DB_NAME="${DB_NAME}"
DB_USER="${DB_USER}"
DB_PASSWORD="${DB_PASSWORD}"
PGLOG="/var/log/postgresql-kindling.log"
PGSOCKET_DIR="${PGSOCKET_DIR}"

mkdir -p "\$(dirname "\$PGLOG")"
mkdir -p "\$PGSOCKET_DIR"
touch "\$PGLOG"
chown postgres:postgres "\$PGLOG" >/dev/null 2>&1 || chown postgres "\$PGLOG" >/dev/null 2>&1 || true
chown postgres:postgres "\$PGSOCKET_DIR" >/dev/null 2>&1 || chown postgres "\$PGSOCKET_DIR" >/dev/null 2>&1 || true

if ! su postgres -c "pg_ctl -D '\$PGDATA' status" >/dev/null 2>&1; then
  su postgres -c "pg_ctl -D '\$PGDATA' -l '\$PGLOG' -o \"-h 127.0.0.1 -p \$PGPORT -c unix_socket_directories=\$PGSOCKET_DIR\" start" >/dev/null
  sleep 1
fi

if su postgres -c "psql -h '\$PGSOCKET_DIR' -tAc \"SELECT 1 FROM pg_roles WHERE rolname = '\$DB_USER'\" postgres" | grep -q 1; then
  su postgres -c "psql -h '\$PGSOCKET_DIR' -v ON_ERROR_STOP=1 -d postgres -c \"ALTER ROLE \$DB_USER WITH LOGIN PASSWORD '\$DB_PASSWORD'\"" >/dev/null
else
  su postgres -c "psql -h '\$PGSOCKET_DIR' -v ON_ERROR_STOP=1 -d postgres -c \"CREATE ROLE \$DB_USER LOGIN PASSWORD '\$DB_PASSWORD'\"" >/dev/null
fi

if ! su postgres -c "psql -h '\$PGSOCKET_DIR' -tAc \"SELECT 1 FROM pg_database WHERE datname = '\$DB_NAME'\" postgres" | grep -q 1; then
  su postgres -c "psql -h '\$PGSOCKET_DIR' -v ON_ERROR_STOP=1 -d postgres -c \"CREATE DATABASE \$DB_NAME OWNER \$DB_USER\"" >/dev/null
fi
EOF
)"
}

stop_postgres() {
  install_postgres

  run_box_sh "$(cat <<EOF
set -eu
PGDATA="${PGDATA}"
if su postgres -c "pg_ctl -D '\$PGDATA' status" >/dev/null 2>&1; then
  su postgres -c "pg_ctl -D '\$PGDATA' stop -m fast" >/dev/null
  echo "postgres stopped in box VM"
else
  echo "postgres already stopped in box VM"
fi
EOF
)"
}

print_status() {
  run_box_sh "$(cat <<EOF
set -eu
PGDATA="${PGDATA}"
if command -v pg_ctl >/dev/null 2>&1 && su postgres -c "pg_ctl -D '\$PGDATA' status" >/dev/null 2>&1; then
  echo "postgres running in box VM"
else
  echo "postgres not running in box VM"
fi
EOF
)"
  cat <<EOF
Forward from macOS with:
  kindling local box port-forward --guest-port ${PGPORT} --host-port ${PGPORT}

Then use:
  ${HOST_DSN}
EOF
}

case "${ACTION}" in
  init|start)
    ensure_kindling
    ensure_box_running
    ensure_started
    cat <<EOF
postgres ready inside the box VM

Forward from macOS with:
  kindling local box port-forward --guest-port ${PGPORT} --host-port ${PGPORT}

Then use:
  ${HOST_DSN}
EOF
    ;;
  stop)
    ensure_kindling
    ensure_box_running
    stop_postgres
    ;;
  status)
    ensure_kindling
    ensure_box_running
    print_status
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "unknown action: ${ACTION}" >&2
    usage >&2
    exit 1
    ;;
esac
