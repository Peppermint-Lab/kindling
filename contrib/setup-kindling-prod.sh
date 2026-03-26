#!/usr/bin/env bash
# One-time (re-runnable) production install: systemd unit + wrapper + /etc/kindling/kindling.env
# Run on the server with sudo, from repo root or any directory if KINDLING_REPO is set.
set -euo pipefail

SERVICE_USER="${SERVICE_USER:-ubuntu}"
KINDLING_HOME="${KINDLING_HOME:-/home/${SERVICE_USER}/kindling}"
REPO="${KINDLING_REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run with sudo: sudo bash contrib/setup-kindling-prod.sh"
  exit 1
fi

install -d -m 0755 /usr/local/lib/kindling
install -m 0755 "${REPO}/contrib/kindling-serve.sh" /usr/local/lib/kindling/serve.sh

install -d -m 0755 /etc/kindling
ENV_DST=/etc/kindling/kindling.env
if [[ ! -f "$ENV_DST" ]]; then
  install -m 0640 -o root -g "${SERVICE_USER}" "${REPO}/contrib/kindling-prod.env.example" "$ENV_DST"
  echo "Created $ENV_DST — edit it (especially KINDLING_ADVERTISE_HOST, KINDLING_PUBLIC_URL, KINDLING_ACME_EMAIL), then:"
  echo "  systemctl daemon-reload && systemctl enable --now kindling"
else
  echo "Leaving existing $ENV_DST unchanged."
fi
chgrp "${SERVICE_USER}" "$ENV_DST" 2>/dev/null || true
chmod 640 "$ENV_DST" 2>/dev/null || true

# systemd expects ubuntu homedir; adjust unit if SERVICE_USER changes.
sed "s/^User=.*/User=${SERVICE_USER}/; s/^Group=.*/Group=${SERVICE_USER}/; s|^WorkingDirectory=.*|WorkingDirectory=${KINDLING_HOME}|" \
  "${REPO}/contrib/systemd/kindling.service" > /etc/systemd/system/kindling.service

systemctl daemon-reload
echo "Installed kindling.service. Run: systemctl enable --now kindling"
echo "After each manual rebuild of bin/kindling (non-Makefile), restore capabilities:"
echo "  sudo setcap cap_net_admin,cap_net_bind_service+ep \"${KINDLING_HOME}/bin/kindling\""
