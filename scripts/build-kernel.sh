#!/usr/bin/env bash
# Build a minimal Linux kernel for Kindling microVMs (native host only — no Docker).
# Requires Linux with kernel build dependencies. On macOS, use: make kernel (prebuilt download).
set -euo pipefail

if [ "$(uname -s)" != "Linux" ]; then
  echo "Native kernel builds are only supported on Linux." >&2
  echo "On macOS use: make kernel  (downloads a prebuilt vmlinuz)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KINDLING_DATA="${KINDLING_DATA:-$HOME/.kindling}"
LINUX_VERSION="6.12.8"
LINUX_BRANCH="ch-${LINUX_VERSION}"

mkdir -p "$KINDLING_DATA"

ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) TARGET_ARCH="arm64"; IMAGE_PATH="arch/arm64/boot/Image" ;;
  x86_64)        TARGET_ARCH="x86_64"; IMAGE_PATH="arch/x86/boot/compressed/vmlinux.bin" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Building Linux kernel ${LINUX_VERSION} for ${TARGET_ARCH} on host..."

if ! command -v make &>/dev/null || ! command -v git &>/dev/null; then
  echo "Install build deps, e.g.: sudo apt-get install -y git build-essential flex bison bc libssl-dev libelf-dev" >&2
  exit 1
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

cd "$WORKDIR"
echo "Cloning kernel (branch: $LINUX_BRANCH)..."
git clone --depth 1 https://github.com/cloud-hypervisor/linux.git -b "$LINUX_BRANCH" linux-src
cd linux-src

echo "Configuring kernel..."
if [ "$TARGET_ARCH" = "arm64" ]; then
  make defconfig
else
  make ch_defconfig
fi

scripts/config --enable CONFIG_VIRTIO
scripts/config --enable CONFIG_VIRTIO_PCI
scripts/config --enable CONFIG_VIRTIO_MMIO
scripts/config --enable CONFIG_VIRTIO_BLK
scripts/config --enable CONFIG_VIRTIO_NET
scripts/config --enable CONFIG_VIRTIO_CONSOLE
scripts/config --enable CONFIG_VHOST_VSOCK
scripts/config --enable CONFIG_VSOCKETS
scripts/config --enable CONFIG_VIRTIO_VSOCKETS
scripts/config --enable CONFIG_VIRTIO_VSOCKETS_COMMON
scripts/config --enable CONFIG_NET
scripts/config --enable CONFIG_INET
scripts/config --enable CONFIG_EXT4_FS
scripts/config --enable CONFIG_TMPFS
scripts/config --enable CONFIG_DEVTMPFS
scripts/config --enable CONFIG_DEVTMPFS_MOUNT
scripts/config --enable CONFIG_BLK_DEV_INITRD
scripts/config --enable CONFIG_RD_GZIP
scripts/config --enable CONFIG_PROC_FS
scripts/config --enable CONFIG_SYSFS
scripts/config --enable CONFIG_FUSE_FS
scripts/config --enable CONFIG_VIRTIO_FS
scripts/config --enable CONFIG_DAX
scripts/config --enable CONFIG_FS_DAX
scripts/config --enable CONFIG_9P_FS
scripts/config --enable CONFIG_9P_FS_POSIX_ACL
scripts/config --enable CONFIG_NET_9P
scripts/config --enable CONFIG_NET_9P_VIRTIO
scripts/config --enable CONFIG_SERIAL_8250
scripts/config --enable CONFIG_SERIAL_8250_CONSOLE
scripts/config --enable CONFIG_HVC_DRIVER
scripts/config --enable CONFIG_VIRTIO_CONSOLE
scripts/config --disable CONFIG_SOUND
scripts/config --disable CONFIG_USB
scripts/config --disable CONFIG_WIRELESS
scripts/config --disable CONFIG_WLAN
scripts/config --disable CONFIG_BLUETOOTH
scripts/config --disable CONFIG_DRM
scripts/config --disable CONFIG_FB
scripts/config --disable CONFIG_INPUT_MOUSE
scripts/config --disable CONFIG_INPUT_JOYSTICK
scripts/config --disable CONFIG_INPUT_TABLET
scripts/config --disable CONFIG_INPUT_TOUCHSCREEN

make olddefconfig

echo "Building kernel..."
if [ "$TARGET_ARCH" = "arm64" ]; then
  make -j"$(nproc)" Image
  cp arch/arm64/boot/Image "$KINDLING_DATA/vmlinuz.bin"
else
  KCFLAGS="-Wa,-mx86-used-note=no" make -j"$(nproc)" bzImage
  cp arch/x86/boot/compressed/vmlinux.bin "$KINDLING_DATA/vmlinuz.bin"
fi

echo "Kernel saved to ${KINDLING_DATA}/vmlinuz.bin"
