#!/usr/bin/env bash
# Provision the remote dev server for Kindling.
# Run once: make remote-provision
set -euo pipefail

echo "=== Kindling dev server provisioning ==="

# System packages
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  curl \
  git \
  jq \
  qemu-utils \
  libguestfs-tools \
  skopeo \
  umoci \
  iptables \
  iproute2 \
  postgresql-client

# Go (latest stable)
GO_VERSION="1.24.4"
if ! go version 2>/dev/null | grep -q "$GO_VERSION"; then
  echo "Installing Go $GO_VERSION..."
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | sudo tar -C /usr/local -xz
  echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
  export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
fi
echo "Go: $(go version)"

# Docker
if ! docker version &>/dev/null; then
  echo "Installing Docker..."
  curl -fsSL https://get.docker.com | sh
  sudo usermod -aG docker "$USER"
fi
echo "Docker: $(docker --version)"

# Cloud Hypervisor
CH_VERSION="v44.0"
if ! cloud-hypervisor --version 2>/dev/null | grep -q "$CH_VERSION"; then
  echo "Installing Cloud Hypervisor $CH_VERSION..."
  curl -fsSL "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${CH_VERSION}/cloud-hypervisor-static" \
    -o /tmp/cloud-hypervisor
  chmod +x /tmp/cloud-hypervisor
  sudo mv /tmp/cloud-hypervisor /usr/local/bin/cloud-hypervisor
fi
echo "Cloud Hypervisor: $(cloud-hypervisor --version)"

# Create data directories (before downloading anything)
sudo mkdir -p /data/base /data/work
sudo chown -R "$USER:$USER" /data

# Kernel/firmware for Cloud Hypervisor
if [ ! -f /data/vmlinuz.bin ]; then
  echo "Downloading hypervisor firmware..."
  curl -fsSL "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.4.2/hypervisor-fw" \
    -o /data/vmlinuz.bin
fi

# Also build a proper kernel for full OS support (runs in background)
if [ ! -f /data/vmlinux-ch.bin ]; then
  echo "Building Cloud Hypervisor kernel (background)..."
  (
    cd /tmp
    git clone --depth 1 https://github.com/cloud-hypervisor/linux.git -b ch-6.12.8 linux-ch 2>/dev/null || true
    cd linux-ch
    make ch_defconfig
    KCFLAGS="-Wa,-mx86-used-note=no" make bzImage -j$(nproc)
    cp arch/x86/boot/compressed/vmlinux.bin /data/vmlinux-ch.bin
    echo "Kernel built: /data/vmlinux-ch.bin"
  ) &
  echo "Kernel build started in background (PID: $!)"
  echo "Check with: ls -la /data/vmlinux-ch.bin"
fi

# PostgreSQL (via Docker)
if ! docker ps | grep -q kindling-postgres; then
  echo "Starting PostgreSQL..."
  docker run -d \
    --name kindling-postgres \
    --restart unless-stopped \
    -p 5432:5432 \
    -e POSTGRES_DB=kindling \
    -e POSTGRES_USER=kindling \
    -e POSTGRES_PASSWORD=kindling \
    -v kindling-pgdata:/var/lib/postgresql/data \
    postgres:17 \
    postgres -c wal_level=logical -c max_replication_slots=10 -c max_wal_senders=10
fi

echo ""
echo "=== Provisioning complete ==="
echo "PostgreSQL: postgres://kindling:kindling@localhost:5432/kindling"
echo "Cloud Hypervisor: $(cloud-hypervisor --version)"
echo "Ready to run: make dev-up"
