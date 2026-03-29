#!/usr/bin/env bash
# Set up host networking for Kindling VMs.
# Enables IP forwarding and iptables masquerade so TAP-backed VMs can reach the internet.
set -euo pipefail

SUDO=""
if [[ "$(id -u)" -ne 0 ]]; then
  SUDO="sudo"
fi

default_iface() {
  ip route show default 0.0.0.0/0 | awk 'NR == 1 { print $5 }'
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

IFACE="${KINDLING_VM_EGRESS_IFACE:-$(default_iface)}"
if [[ -z "$IFACE" ]]; then
  echo "ERROR: Could not detect default network interface"
  exit 1
fi

CIDRS_RAW="${KINDLING_VM_EGRESS_CIDRS:-10.0.0.0/8}"
TAP_PREFIXES_RAW="${KINDLING_VM_TAP_PREFIXES:-kch+,kci+}"

IFS=',' read -r -a CIDRS <<<"$CIDRS_RAW"
IFS=',' read -r -a TAP_PREFIXES <<<"$TAP_PREFIXES_RAW"

echo "Configuring networking for Kindling VMs (interface: $IFACE)..."

$SUDO sysctl -w net.ipv4.ip_forward=1
printf 'net.ipv4.ip_forward=1\n' | $SUDO tee /etc/sysctl.d/99-kindling.conf >/dev/null

for cidr in "${CIDRS[@]}"; do
  cidr="$(trim "$cidr")"
  [[ -n "$cidr" ]] || continue
  $SUDO iptables -t nat -C POSTROUTING -s "$cidr" -o "$IFACE" -j MASQUERADE 2>/dev/null || \
    $SUDO iptables -t nat -A POSTROUTING -s "$cidr" -o "$IFACE" -j MASQUERADE
done

for prefix in "${TAP_PREFIXES[@]}"; do
  prefix="$(trim "$prefix")"
  [[ -n "$prefix" ]] || continue
  $SUDO iptables -C FORWARD -i "$prefix" -o "$IFACE" -j ACCEPT 2>/dev/null || \
    $SUDO iptables -A FORWARD -i "$prefix" -o "$IFACE" -j ACCEPT
  $SUDO iptables -C FORWARD -i "$IFACE" -o "$prefix" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || \
    $SUDO iptables -A FORWARD -i "$IFACE" -o "$prefix" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
done

echo "Networking configured."
echo "  IP forwarding: enabled"
echo "  Egress interface: $IFACE"
echo "  NAT CIDRs: $CIDRS_RAW"
echo "  TAP prefixes: $TAP_PREFIXES_RAW"
