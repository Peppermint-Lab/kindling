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

# Kernel for Cloud Hypervisor
sudo mkdir -p /data
if [ ! -f /data/vmlinuz.bin ]; then
  echo "Downloading Cloud Hypervisor kernel..."
  curl -fsSL "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${CH_VERSION}/hypervisor-fw" \
    -o /data/vmlinuz.bin
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

# Create data directories
sudo mkdir -p /data/base /data/work
sudo chown -R "$USER:$USER" /data

echo ""
echo "=== Provisioning complete ==="
echo "PostgreSQL: postgres://kindling:kindling@localhost:5432/kindling"
echo "Cloud Hypervisor: $(cloud-hypervisor --version)"
echo "Ready to run: make dev-up"
