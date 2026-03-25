#!/usr/bin/env bash
# Set up host networking for Kindling VMs.
# Enables IP forwarding and iptables masquerade so VMs can reach the internet.
set -euo pipefail

# Detect default interface
IFACE=$(ip route | grep default | awk '{print $5}' | head -1)
if [ -z "$IFACE" ]; then
  echo "ERROR: Could not detect default network interface"
  exit 1
fi

echo "Configuring networking for VMs (interface: $IFACE)..."

# Enable IP forwarding
sudo sysctl -w net.ipv4.ip_forward=1
echo "net.ipv4.ip_forward=1" | sudo tee /etc/sysctl.d/99-kindling.conf

# iptables masquerade for VM traffic (10.0.0.0/8 covers all /20 ranges)
sudo iptables -t nat -C POSTROUTING -s 10.0.0.0/8 -o "$IFACE" -j MASQUERADE 2>/dev/null || \
  sudo iptables -t nat -A POSTROUTING -s 10.0.0.0/8 -o "$IFACE" -j MASQUERADE

# Allow forwarding for VM traffic
sudo iptables -C FORWARD -s 10.0.0.0/8 -j ACCEPT 2>/dev/null || \
  sudo iptables -A FORWARD -s 10.0.0.0/8 -j ACCEPT
sudo iptables -C FORWARD -d 10.0.0.0/8 -j ACCEPT 2>/dev/null || \
  sudo iptables -A FORWARD -d 10.0.0.0/8 -j ACCEPT

echo "Networking configured."
echo "  IP forwarding: enabled"
echo "  Masquerade: 10.0.0.0/8 → $IFACE"
