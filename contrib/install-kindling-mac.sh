#!/usr/bin/env bash
set -euo pipefail

REPO="${KINDLING_REPO:-Peppermint-Lab/kindling}"
REF="${KINDLING_REF:-main}"
KERNEL_RELEASE="${KINDLING_KERNEL_RELEASE:-kernel-v0.1.0}"
INSTALL_DIR="${KINDLING_INSTALL_DIR:-$HOME/.local/bin}"
ASSET_DIR="${KINDLING_ASSET_DIR:-$HOME/.kindling-mac}"
CONFIG_PATH="${KINDLING_CONFIG_PATH:-$HOME/.kindling-mac.yaml}"
ROOTFS_URL="${KINDLING_ROOTFS_URL:-https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/aarch64/alpine-minirootfs-3.23.3-aarch64.tar.gz}"
SOURCE_URL="${KINDLING_SOURCE_URL:-https://codeload.github.com/${REPO}/tar.gz/${REF}}"
NO_MODIFY_PATH="${KINDLING_NO_MODIFY_PATH:-0}"
TMP_DIR=""

usage() {
  cat <<'EOF'
Usage: install-kindling-mac.sh [options]

Options:
  --install-dir PATH      Install binaries into PATH
  --asset-dir PATH        Download guest assets into PATH
  --config-path PATH      Install the sample config at PATH
  --no-modify-path        Do not update shell config even if PATH is missing the install dir
  -h, --help              Show this help text

Environment overrides:
  KINDLING_INSTALL_DIR
  KINDLING_ASSET_DIR
  KINDLING_CONFIG_PATH
  KINDLING_NO_MODIFY_PATH=1
  KINDLING_REPO
  KINDLING_REF
  KINDLING_KERNEL_RELEASE
  KINDLING_ROOTFS_URL
  KINDLING_SOURCE_URL
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

install_file() {
  local src="$1"
  local dst="$2"

  if [[ -d "$INSTALL_DIR" && -w "$INSTALL_DIR" ]]; then
    install -m 0755 "$src" "$dst"
    return
  fi

  if [[ ! -d "$INSTALL_DIR" ]]; then
    if mkdir -p "$INSTALL_DIR" 2>/dev/null; then
      install -m 0755 "$src" "$dst"
      return
    fi
  fi

  sudo mkdir -p "$INSTALL_DIR"
  sudo install -m 0755 "$src" "$dst"
}

need_sudo_for_path() {
  local path="$1"
  local parent

  if [[ -e "$path" ]]; then
    [[ ! -w "$path" ]]
    return
  fi

  parent="$(dirname "$path")"
  while [[ ! -d "$parent" && "$parent" != "/" ]]; do
    parent="$(dirname "$parent")"
  done

  [[ ! -w "$parent" ]]
}

cleanup() {
  if [[ -n "${TMP_DIR}" && -d "${TMP_DIR}" ]]; then
    rm -rf "${TMP_DIR}"
  fi
}

path_contains() {
  local dir="$1"
  [[ ":$PATH:" == *":$dir:"* ]]
}

shell_rc_file() {
  local shell_name

  shell_name="$(basename "${SHELL:-}")"
  case "$shell_name" in
    zsh) printf '%s\n' "$HOME/.zshrc" ;;
    bash) printf '%s\n' "$HOME/.bashrc" ;;
    fish) printf '%s\n' "$HOME/.config/fish/config.fish" ;;
    *) printf '%s\n' "$HOME/.profile" ;;
  esac
}

path_plan() {
  local rc_file="$1"

  if path_contains "$INSTALL_DIR"; then
    printf '%s\n' "already on PATH"
    return
  fi

  if [[ "$NO_MODIFY_PATH" = "1" ]]; then
    printf '%s\n' "will not modify PATH (per --no-modify-path)"
    return
  fi

  printf '%s\n' "will add to PATH via ${rc_file}"
}

ensure_path() {
  local rc_file="$1"
  local export_line

  if path_contains "$INSTALL_DIR"; then
    return
  fi

  if [[ "$NO_MODIFY_PATH" = "1" ]]; then
    return
  fi

  mkdir -p "$(dirname "$rc_file")"

  if [[ "$rc_file" == *.fish ]]; then
    export_line="fish_add_path \"$INSTALL_DIR\""
  else
    export_line="export PATH=\"$INSTALL_DIR:\$PATH\""
  fi

  if [[ -f "$rc_file" ]] && grep -Fqx "$export_line" "$rc_file"; then
    return
  fi

  {
    printf '\n'
    printf '%s\n' "$export_line"
  } >>"$rc_file"

  echo "Added ${INSTALL_DIR} to PATH in ${rc_file}."
}

main() {
  local os arch source_tar src_dir kernel_url initramfs_url rc_file shell_name

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --install-dir)
        INSTALL_DIR="$2"
        shift 2
        ;;
      --asset-dir)
        ASSET_DIR="$2"
        shift 2
        ;;
      --config-path)
        CONFIG_PATH="$2"
        shift 2
        ;;
      --no-modify-path)
        NO_MODIFY_PATH=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "unknown option: $1" >&2
        usage >&2
        exit 1
        ;;
    esac
  done

  os="$(uname -s)"
  arch="$(uname -m)"
  if [[ "$os" != "Darwin" ]]; then
    echo "kindling-mac install only supports macOS" >&2
    exit 1
  fi
  if [[ "$arch" != "arm64" ]]; then
    echo "kindling-mac currently supports Apple Silicon only" >&2
    exit 1
  fi

  require_cmd curl
  require_cmd tar
  require_cmd make
  require_cmd go
  require_cmd install
  require_cmd codesign

  shell_name="$(basename "${SHELL:-unknown}")"
  rc_file="$(shell_rc_file)"

  cat <<EOF
kindling-mac installer
  shell: ${shell_name}
  shell rc: ${rc_file}
  install dir: ${INSTALL_DIR}
  asset dir: ${ASSET_DIR}
  config path: ${CONFIG_PATH}
  PATH handling: $(path_plan "$rc_file")

EOF

  TMP_DIR="$(mktemp -d)"
  trap cleanup EXIT

  source_tar="$TMP_DIR/kindling.tar.gz"
  kernel_url="https://github.com/${REPO}/releases/download/${KERNEL_RELEASE}/vmlinuz-arm64"
  initramfs_url="https://github.com/${REPO}/releases/download/${KERNEL_RELEASE}/initramfs-arm64.cpio.gz"

  echo "Downloading Kindling source from ${SOURCE_URL}..."
  curl -fsSL "$SOURCE_URL" -o "$source_tar"
  tar -xzf "$source_tar" -C "$TMP_DIR"
  src_dir="$(find "$TMP_DIR" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
  if [[ -z "$src_dir" ]]; then
    echo "unable to locate extracted source tree" >&2
    exit 1
  fi

  echo "Building kindling and kindling-mac..."
  (
    cd "$src_dir"
    make build kindling-mac
  )

  echo "Installing binaries to ${INSTALL_DIR}..."
  install_file "$src_dir/bin/kindling" "${INSTALL_DIR}/kindling"
  install_file "$src_dir/bin/kindling-mac" "${INSTALL_DIR}/kindling-mac"
  ensure_path "$rc_file"

  echo "Downloading guest assets into ${ASSET_DIR}..."
  mkdir -p "$ASSET_DIR"
  curl -fsSL "$kernel_url" -o "${ASSET_DIR}/vmlinuz"
  curl -fsSL "$initramfs_url" -o "${ASSET_DIR}/initramfs.cpio.gz"
  curl -fsSL "$ROOTFS_URL" -o "${ASSET_DIR}/rootfs.tar.gz"

  if [[ -f "$CONFIG_PATH" ]]; then
    echo "Config already exists at ${CONFIG_PATH}; leaving it in place."
  else
    echo "Installing sample config to ${CONFIG_PATH}..."
    if need_sudo_for_path "$CONFIG_PATH"; then
      sudo install -D -m 0644 "$src_dir/contrib/kindling-mac.yaml" "$CONFIG_PATH"
    else
      mkdir -p "$(dirname "$CONFIG_PATH")"
      install -m 0644 "$src_dir/contrib/kindling-mac.yaml" "$CONFIG_PATH"
    fi
  fi

  cat <<EOF

kindling-mac is installed.

Next steps:
  1. Edit ${CONFIG_PATH} if you want to change the defaults.
  2. Start the background daemon:
       kindling-mac
  3. In another terminal:
       kindling local box start
EOF

  if ! path_contains "$INSTALL_DIR"; then
    if [[ "$NO_MODIFY_PATH" = "1" ]]; then
      cat <<EOF

Your current PATH does not include ${INSTALL_DIR}.
Add it manually before using 'kindling' and 'kindling-mac'.
EOF
    else
      cat <<EOF

Open a new shell, or run:
  source ${rc_file}
EOF
    fi
  fi
}

main "$@"
