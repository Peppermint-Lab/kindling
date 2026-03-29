#!/usr/bin/env bash
# Build the guest agent initramfs for Cloud Hypervisor VMs.
# Run on the Linux dev server (requires root for cpio).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
OUTPUT="/data/initramfs.cpio.gz"
TMPDIR=$(mktemp -d)

echo "Building guest agent..."
cd "$PROJECT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$TMPDIR/init" ./cmd/guest-agent

echo "Creating initramfs rootfs..."
mkdir -p "$TMPDIR/rootfs"/{bin,sbin,etc,proc,sys,dev,tmp,app,usr/bin,usr/sbin}

# Copy guest agent as /init (PID 1)
cp "$TMPDIR/init" "$TMPDIR/rootfs/init"
chmod +x "$TMPDIR/rootfs/init"

# Add busybox for basic utilities (ip, sh, etc.)
if command -v busybox.static &>/dev/null; then
  cp "$(which busybox.static)" "$TMPDIR/rootfs/bin/busybox"
  chmod +x "$TMPDIR/rootfs/bin/busybox"
  # Create symlinks for essential commands
  for cmd in sh ip ifconfig route ping wget cat ls mkdir mount umount chroot; do
    ln -sf busybox "$TMPDIR/rootfs/bin/$cmd"
  done
elif [ -f /bin/busybox.static ]; then
  cp /bin/busybox.static "$TMPDIR/rootfs/bin/busybox"
  chmod +x "$TMPDIR/rootfs/bin/busybox"
  # Create symlinks for essential commands
  for cmd in sh ip ifconfig route ping wget cat ls mkdir mount umount chroot; do
    ln -sf busybox "$TMPDIR/rootfs/bin/$cmd"
  done
elif command -v busybox &>/dev/null; then
  cp "$(which busybox)" "$TMPDIR/rootfs/bin/busybox"
  chmod +x "$TMPDIR/rootfs/bin/busybox"
  # Create symlinks for essential commands
  for cmd in sh ip ifconfig route ping wget cat ls mkdir mount umount chroot; do
    ln -sf busybox "$TMPDIR/rootfs/bin/$cmd"
  done
elif [ -f /bin/busybox ]; then
  cp /bin/busybox "$TMPDIR/rootfs/bin/busybox"
  chmod +x "$TMPDIR/rootfs/bin/busybox"
  for cmd in sh ip ifconfig route ping wget cat ls mkdir mount umount chroot; do
    ln -sf busybox "$TMPDIR/rootfs/bin/$cmd"
  done
else
  echo "WARNING: busybox not found — guest will have limited utilities"
  # Copy essential binaries from host
  for bin in /usr/sbin/ip /bin/sh; do
    if [ -f "$bin" ]; then
      cp "$bin" "$TMPDIR/rootfs${bin}"
    fi
  done
fi

# DNS resolver
mkdir -p "$TMPDIR/rootfs/etc"
echo "nameserver 8.8.8.8" > "$TMPDIR/rootfs/etc/resolv.conf"

echo "Packing initramfs..."
cd "$TMPDIR/rootfs"
find . | cpio -o -H newc 2>/dev/null | gzip > "$OUTPUT"

echo "Cleaning up..."
rm -rf "$TMPDIR"

SIZE=$(du -h "$OUTPUT" | cut -f1)
echo "Built initramfs: $OUTPUT ($SIZE)"
