#!/usr/bin/env bash
# Build a minimal Linux kernel for Kindling microVMs.
# Supports both x86_64 and arm64 (auto-detects host).
# Runs inside Docker for reproducibility.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
KINDLING_DATA="${KINDLING_DATA:-$HOME/.kindling}"
LINUX_VERSION="6.12.8"
LINUX_BRANCH="ch-${LINUX_VERSION}"

mkdir -p "$KINDLING_DATA"

ARCH=$(uname -m)
case "$ARCH" in
  arm64|aarch64) TARGET_ARCH="arm64"; CROSS_COMPILE=""; IMAGE_PATH="arch/arm64/boot/Image" ;;
  x86_64)        TARGET_ARCH="x86_64"; CROSS_COMPILE=""; IMAGE_PATH="arch/x86/boot/compressed/vmlinux.bin" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Building Linux kernel ${LINUX_VERSION} for ${TARGET_ARCH}..."

# Build kernel inside Docker for clean environment
docker run --rm \
  --platform "linux/${TARGET_ARCH}" \
  -v "$KINDLING_DATA:/out" \
  -e TARGET_ARCH="$TARGET_ARCH" \
  -e IMAGE_PATH="$IMAGE_PATH" \
  -e LINUX_BRANCH="$LINUX_BRANCH" \
  alpine:3.21 sh -c '
    set -eu

    echo "Installing build dependencies..."
    apk add --no-cache git build-base flex bison bc perl linux-headers openssl-dev elfutils-dev bash

    echo "Cloning kernel (branch: $LINUX_BRANCH)..."
    cd /tmp
    git clone --depth 1 https://github.com/cloud-hypervisor/linux.git -b "$LINUX_BRANCH" linux-src
    cd linux-src

    echo "Configuring kernel..."
    if [ "$TARGET_ARCH" = "arm64" ]; then
      make defconfig
    else
      make defconfig
    fi

    # Enable virtio and vsock
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

    # Disable unnecessary features for smaller kernel
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
      make -j$(nproc) Image
      cp arch/arm64/boot/Image /out/vmlinuz.bin
    else
      make -j$(nproc) vmlinux
      cp vmlinux /out/vmlinuz.bin
    fi
    echo ""
    echo "Kernel built: /out/vmlinuz.bin ($(du -h /out/vmlinuz.bin | cut -f1))"
  '

echo "Kernel saved to ${KINDLING_DATA}/vmlinuz.bin"
