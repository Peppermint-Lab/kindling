#!/usr/bin/env bash
# Build guest-agent initramfs without Docker. Requires: go, gzip, GNU cpio (cpio -H newc).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
KINDLING_DATA="${KINDLING_DATA:-$HOME/.kindling}"
mkdir -p "$KINDLING_DATA"

if command -v gcpio >/dev/null 2>&1; then
  CPIO=gcpio
elif cpio --version 2>&1 | grep -q GNU; then
  CPIO=cpio
elif cpio --help 2>&1 | grep -q -- '--format .*newc'; then
  CPIO=cpio
else
  echo "error: cpio with newc format support is required for initramfs." >&2
  echo "  macOS: install GNU cpio or use BSD cpio with --format newc support" >&2
  echo "  Or use prebuilt: make initramfs-download" >&2
  exit 1
fi

GOARCH_TARGET="amd64"
case "$(uname -m)" in
  arm64|aarch64) GOARCH_TARGET="arm64" ;;
esac

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Cross-compiling guest agent for Linux/$GOARCH_TARGET..."
cd "$PROJECT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH_TARGET go build -o "$TMPDIR/init" ./cmd/guest-agent

echo "Creating initramfs rootfs..."
mkdir -p "$TMPDIR/rootfs"/{bin,sbin,etc,proc,sys,dev,tmp,app,usr/bin,usr/sbin}
cp "$TMPDIR/init" "$TMPDIR/rootfs/init"
chmod +x "$TMPDIR/rootfs/init"

busybox_copied=0
if command -v busybox.static &>/dev/null; then
  cp "$(command -v busybox.static)" "$TMPDIR/rootfs/bin/busybox"
  busybox_copied=1
elif [ -f /bin/busybox.static ]; then
  cp /bin/busybox.static "$TMPDIR/rootfs/bin/busybox"
  busybox_copied=1
elif command -v busybox &>/dev/null; then
  cp "$(command -v busybox)" "$TMPDIR/rootfs/bin/busybox"
  busybox_copied=1
elif [ -f /bin/busybox ]; then
  cp /bin/busybox "$TMPDIR/rootfs/bin/busybox"
  busybox_copied=1
fi
if [ "$busybox_copied" -eq 1 ]; then
  chmod +x "$TMPDIR/rootfs/bin/busybox"
fi
if [ "$busybox_copied" -eq 1 ]; then
  for cmd in sh ip ifconfig route ping cat ls mkdir mount umount chroot; do
    ln -sf busybox "$TMPDIR/rootfs/bin/$cmd"
  done
else
  echo "warning: busybox not found — guest will have limited utilities"
fi

mkdir -p "$TMPDIR/rootfs/etc"
echo "nameserver 8.8.8.8" > "$TMPDIR/rootfs/etc/resolv.conf"

echo "Packing initramfs with $CPIO..."
cd "$TMPDIR/rootfs"
find . | $CPIO -o -H newc 2>/dev/null | gzip > "$KINDLING_DATA/initramfs.cpio.gz"

echo "Initramfs built: $KINDLING_DATA/initramfs.cpio.gz"
