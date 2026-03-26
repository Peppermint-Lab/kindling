#!/usr/bin/env bash
# Install host tools Kindling needs (OCI build/export, microVMs, optional Postgres).
#
# Usage:
#   sudo ./contrib/install-host-deps.sh              # core toolchain only (no Postgres, no cloud-hypervisor)
#   sudo ./contrib/install-host-deps.sh --all        # full single-node server: core + Postgres + Cloud Hypervisor
#   sudo ./contrib/install-host-deps.sh --with-postgres
#   sudo ./contrib/install-host-deps.sh --with-cloud-hypervisor
#
# macOS (no sudo for Homebrew): ./contrib/install-host-deps.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_PATH="${SCRIPT_DIR}/$(basename "${BASH_SOURCE[0]}")"

WITH_POSTGRES=0
WITH_CH=0
ALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      ALL=1
      shift
      ;;
    --with-postgres) WITH_POSTGRES=1; shift ;;
    --with-cloud-hypervisor) WITH_CH=1; shift ;;
    -h|--help)
      sed -n '1,20p' "$0"
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 1
      ;;
  esac
done

if [[ "$ALL" -eq 1 ]]; then
  WITH_POSTGRES=1
  WITH_CH=1
fi

CH_VERSION="${KINDLING_CH_VERSION:-v44.0}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1
}

verify_core() {
  local missing=()
  if [[ "$(uname -s)" = Darwin ]]; then
    for c in skopeo umoci; do
      require_cmd "$c" || missing+=("$c")
    done
  else
    for c in buildah skopeo umoci; do
      require_cmd "$c" || missing+=("$c")
    done
    require_cmd crun || missing+=("crun")
    require_cmd virt-make-fs || missing+=("virt-make-fs")
    require_cmd qemu-img || missing+=("qemu-img")
  fi
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "warning: missing on PATH: ${missing[*]}" >&2
    return 1
  fi
  echo "OK: core Kindling host tools present."
}

install_linux_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  local pkgs=(
    build-essential curl git jq
    qemu-utils
    libguestfs-tools
    skopeo umoci crun buildah
    iptables iproute2
  )
  if [[ "$WITH_POSTGRES" -eq 1 ]]; then
    pkgs+=(postgresql postgresql-client)
  else
    pkgs+=(postgresql-client)
  fi
  apt-get install -y "${pkgs[@]}"
}

install_linux_dnf() {
  local extra=()
  if [[ "$WITH_POSTGRES" -eq 1 ]]; then
    extra+=(postgresql-server postgresql)
  fi
  dnf install -y \
    '@development tools' curl git jq \
    qemu-img libguestfs-tools \
    skopeo umoci crun buildah \
    iptables iproute \
    "${extra[@]:-}"
  if [[ "$WITH_POSTGRES" -eq 1 ]]; then
    echo "Configure PostgreSQL for wal_level=logical and kindling user/db (see install-host-deps.sh bootstrap for apt)." >&2
  fi
}

install_cloud_hypervisor() {
  if command -v cloud-hypervisor >/dev/null 2>&1 && cloud-hypervisor --version 2>/dev/null | grep -q "$CH_VERSION"; then
    echo "cloud-hypervisor already installed"
    return 0
  fi
  echo "Installing Cloud Hypervisor $CH_VERSION..."
  curl -fsSL "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${CH_VERSION}/cloud-hypervisor-static" \
    -o /tmp/cloud-hypervisor-kindling
  chmod +x /tmp/cloud-hypervisor-kindling
  mv /tmp/cloud-hypervisor-kindling /usr/local/bin/cloud-hypervisor
  setcap cap_net_admin+ep /usr/local/bin/cloud-hypervisor 2>/dev/null || true
  echo "Cloud Hypervisor: $(cloud-hypervisor --version)"
}

bootstrap_postgres_apt() {
  systemctl enable --now postgresql || true
  if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='kindling'" | grep -q 1; then
    sudo -u postgres psql -c "CREATE ROLE kindling WITH LOGIN PASSWORD 'kindling';" || true
  fi
  if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='kindling'" | grep -q 1; then
    sudo -u postgres psql -c "CREATE DATABASE kindling OWNER kindling;"
  fi
  sudo -u postgres psql -c "ALTER SYSTEM SET wal_level = 'logical';" || true
  sudo -u postgres psql -c "ALTER SYSTEM SET max_replication_slots = 10;" || true
  sudo -u postgres psql -c "ALTER SYSTEM SET max_wal_senders = 10;" || true
  systemctl restart postgresql || true
}

setup_data_dirs() {
  mkdir -p /data/base /data/work
  local owner="${SUDO_USER:-root}"
  if id "$owner" &>/dev/null; then
    chown -R "$owner:$owner" /data
  fi
}

install_darwin_brew() {
  if ! require_cmd brew; then
    echo "Install Homebrew first: https://brew.sh" >&2
    exit 1
  fi
  brew install skopeo umoci gnu-cpio
  echo ""
  echo "macOS: use Apple Virtualization Framework for dev (Cloud Hypervisor is Linux+KVM)."
  echo "Install Go 1.26+ and Postgres locally (e.g. brew install postgresql@17) or use contrib/dev-postgres.sh"
  echo ""
  echo "OCI image builds use buildah, which is not packaged in Homebrew on macOS."
  echo "Install buildah on your Linux Kindling server: sudo ./contrib/install-host-deps.sh (core includes buildah)."
  echo "Remote dev: make remote-provision"
}

# --- main ---
OS="$(uname -s)"
if [[ "$OS" = Darwin ]]; then
  install_darwin_brew
  verify_core || true
  exit 0
fi

if [[ "$OS" != Linux ]]; then
  echo "Unsupported OS: $OS" >&2
  exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
  exec sudo /bin/bash "$SCRIPT_PATH" "$@"
fi

if require_cmd apt-get; then
  install_linux_apt
elif require_cmd dnf; then
  install_linux_dnf
else
  echo "No apt-get or dnf found. Install buildah, skopeo, umoci, crun, libguestfs-tools, qemu-utils manually." >&2
  exit 1
fi

if [[ "$WITH_CH" -eq 1 ]]; then
  install_cloud_hypervisor
  setup_data_dirs
fi

if [[ "$WITH_POSTGRES" -eq 1 ]] && require_cmd apt-get; then
  bootstrap_postgres_apt
fi

if ls /boot/vmlinuz-* &>/dev/null; then
  chmod a+r /boot/vmlinuz-* 2>/dev/null || true
fi

verify_core

echo ""
echo "=== Kindling host dependencies installed ==="
if [[ "$WITH_POSTGRES" -eq 1 ]]; then
  echo "PostgreSQL: postgres://kindling:kindling@localhost:5432/kindling"
fi
if [[ "$WITH_CH" -eq 1 ]]; then
  echo "Cloud Hypervisor: $(command -v cloud-hypervisor && cloud-hypervisor --version || echo n/a)"
  echo "Bare metal: sudo setcap cap_net_admin+ep bin/kindling  (for TAP) as needed."
fi
