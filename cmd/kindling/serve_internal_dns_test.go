package main

import (
	"context"
	"net/netip"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestInternalDNSEnabledForRuntime(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "")
	if !internalDNSEnabledForRuntime("cloud-hypervisor") {
		t.Fatal("expected cloud-hypervisor runtime to enable internal dns by default")
	}
	if internalDNSEnabledForRuntime("crun") {
		t.Fatal("expected non-cloud-hypervisor runtime to disable internal dns")
	}
}

func TestInternalDNSEnabledForRuntimeHonorsDisableFlag(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "off")
	if internalDNSEnabledForRuntime("cloud-hypervisor") {
		t.Fatal("expected internal dns to be disabled when KINDLING_INTERNAL_DNS_ADDR=off")
	}
}

func TestInternalDNSRuntimeMetadataIncludesBindAddress(t *testing.T) {
	t.Setenv("KINDLING_INTERNAL_DNS_ADDR", "127.0.0.1:1053")
	got := internalDNSRuntimeMetadata("cloud-hypervisor")
	want := map[string]any{
		"internal_dns_enabled": true,
		"internal_dns_addr":    "127.0.0.1:1053",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected metadata:\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" 1.1.1.1 , ,8.8.8.8:53 ")
	want := []string{"1.1.1.1", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected split result:\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestEffectiveInternalDNSAddrDefaultsToPort53(t *testing.T) {
	if got := effectiveInternalDNSAddr(""); got != ":53" {
		t.Fatalf("effective address = %q, want :53", got)
	}
}

type fakeInternalDNSServerStore struct {
	server queries.Server
	found  bool
	err    error
}

func (f fakeInternalDNSServerStore) FindServerByID(ctx context.Context, serverID uuid.UUID) (queries.Server, bool, error) {
	return f.server, f.found, f.err
}

func TestResolveInternalDNSAllowedPrefixUsesRegisteredServerRange(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	want := netip.MustParsePrefix("10.0.16.0/20")
	got, err := resolveInternalDNSAllowedPrefix(context.Background(), fakeInternalDNSServerStore{
		found: true,
		server: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: want,
		},
	}, serverID)
	if err != nil {
		t.Fatalf("resolveInternalDNSAllowedPrefix: %v", err)
	}
	if got != want {
		t.Fatalf("allowed prefix = %s, want %s", got, want)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}
