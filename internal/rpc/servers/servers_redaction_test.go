package servers

import (
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestRedactServerSummary(t *testing.T) {
	t.Parallel()

	summary := serverSummaryOut{
		Server: queries.Server{
			ID:                 pgtype.UUID{Bytes: uuid.MustParse("d0000000-0000-4000-a000-000000000001"), Valid: true},
			Hostname:           "worker-1",
			InternalIp:         "10.0.0.5",
			IpRange:            netip.MustParsePrefix("10.0.0.0/24"),
			WireguardIp:        netip.MustParseAddr("100.64.0.5"),
			WireguardPublicKey: "pubkey",
			WireguardEndpoint:  "vpn.example.com:51820",
			Status:             "active",
		},
		Runtime:           "cloud-hypervisor",
		EnabledComponents: []string{"worker", "edge"},
		Components: []serverComponentOut{{
			Component:        "worker",
			Enabled:          true,
			Health:           "healthy",
			RawStatus:        "healthy",
			LastErrorMessage: "boom",
			Metadata:         map[string]any{"runtime": "cloud-hypervisor"},
		}},
	}

	redacted := redactServerSummary(summary)

	if redacted.InternalIp != "" {
		t.Fatalf("InternalIp = %q, want empty", redacted.InternalIp)
	}
	if redacted.IpRange != (netip.Prefix{}) {
		t.Fatalf("IpRange = %v, want zero", redacted.IpRange)
	}
	if redacted.WireguardIp != (netip.Addr{}) {
		t.Fatalf("WireguardIp = %v, want zero", redacted.WireguardIp)
	}
	if redacted.WireguardPublicKey != "" {
		t.Fatalf("WireguardPublicKey = %q, want empty", redacted.WireguardPublicKey)
	}
	if redacted.WireguardEndpoint != "" {
		t.Fatalf("WireguardEndpoint = %q, want empty", redacted.WireguardEndpoint)
	}
	if redacted.Runtime != "" {
		t.Fatalf("Runtime = %q, want empty", redacted.Runtime)
	}
	if len(redacted.EnabledComponents) != 0 {
		t.Fatalf("EnabledComponents = %v, want empty", redacted.EnabledComponents)
	}
	if len(redacted.Components) != 0 {
		t.Fatalf("Components = %v, want empty", redacted.Components)
	}
}
