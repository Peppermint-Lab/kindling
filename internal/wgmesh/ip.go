package wgmesh

import (
	"crypto/sha256"
	"encoding/binary"
	"net/netip"

	"github.com/google/uuid"
)

// OverlayIP returns the deterministic WireGuard overlay IPv4 for a server UUID
// (10.64.0.0/16 allocation per product spec).
func OverlayIP(id uuid.UUID) netip.Addr {
	h := sha256.Sum256(id[:])
	n := binary.BigEndian.Uint32(h[:4])
	offset := (n % 65534) + 1
	return netip.AddrFrom4([4]byte{10, 64, byte(offset >> 8), byte(offset)})
}
