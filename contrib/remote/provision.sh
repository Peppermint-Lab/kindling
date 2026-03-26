#!/usr/bin/env bash
# Provision the remote dev server for Kindling (after host deps are installed).
# Run once: make remote-provision
#
# Host packages + Postgres + Cloud Hypervisor: contrib/install-host-deps.sh --all
# (Makefile runs that via scp + ssh before this script.)
set -euo pipefail

echo "=== Kindling dev server provisioning (Go + firmware + kernel) ==="

# Go (latest stable)
GO_VERSION="1.24.4"
if ! go version 2>/dev/null | grep -q "$GO_VERSION"; then
  echo "Installing Go $GO_VERSION..."
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | sudo tar -C /usr/local -xz
  echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
  export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
fi
echo "Go: $(go version)"

# Kernel/firmware for Cloud Hypervisor
sudo mkdir -p /data/base /data/work
sudo chown -R "$USER:$USER" /data

if [ ! -f /data/vmlinuz.bin ]; then
  echo "Downloading hypervisor firmware..."
  curl -fsSL "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.4.2/hypervisor-fw" \
    -o /data/vmlinuz.bin
fi

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

if ls /boot/vmlinuz-* &>/dev/null; then
  sudo chmod a+r /boot/vmlinuz-* || true
fi

echo ""
echo "=== Provisioning complete ==="
echo "PostgreSQL: postgres://kindling:kindling@localhost:5432/kindling"
echo "Cloud Hypervisor: $(cloud-hypervisor --version 2>/dev/null || echo 'n/a — run install-host-deps.sh --all')"
echo "Ready to run: make dev-up"
echo "Bare metal: after go build, run: sudo setcap cap_net_admin+ep bin/kindling"
echo "  (so cloud-hypervisor can create TAP devices as non-root.)"
echo "Optional: export KINDLING_RUNTIME=crun to force crun instead of cloud-hypervisor when KVM is present."
