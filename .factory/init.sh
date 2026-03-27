#!/bin/bash
set -e

cd "$(dirname "$0")/.."

# Verify Go toolchain
go version >/dev/null 2>&1 || { echo "Go is not installed"; exit 1; }

# Ensure dependencies are up to date
go mod download
