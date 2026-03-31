package wgmesh

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOverlayIP_Deterministic(t *testing.T) {
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	a := OverlayIP(id)
	b := OverlayIP(id)
	if a != b {
		t.Fatalf("expected deterministic IP, got %v vs %v", a, b)
	}
	if !a.Is4() || !netip.PrefixFrom(a, 32).IsValid() {
		t.Fatalf("expected IPv4 /32 candidate, got %v", a)
	}
	if !strings.HasPrefix(a.String(), "10.64.") {
		t.Fatalf("expected 10.64.0.0/16 overlay, got %s", a)
	}
}
