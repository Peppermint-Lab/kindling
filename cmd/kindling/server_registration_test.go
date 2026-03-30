package main

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type fakeServerRegistrationStore struct {
	findServer          queries.Server
	found               bool
	findErr             error
	otherServerCount    int
	countErr            error
	allocateAndRegister queries.Server
	allocateErr         error
	reRegister          queries.Server
	reRegisterErr       error
	ensureErr           error

	allocateCalls int
	reuseCalls    int
	ensureCalls   int
}

func (f *fakeServerRegistrationStore) FindServerByID(ctx context.Context, serverID uuid.UUID) (queries.Server, bool, error) {
	return f.findServer, f.found, f.findErr
}

func (f *fakeServerRegistrationStore) CountOtherServers(ctx context.Context, serverID uuid.UUID) (int, error) {
	return f.otherServerCount, f.countErr
}

func (f *fakeServerRegistrationStore) RegisterExistingServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string, ipRange netip.Prefix) (queries.Server, error) {
	f.reuseCalls++
	return f.reRegister, f.reRegisterErr
}

func (f *fakeServerRegistrationStore) AllocateAndRegisterServer(ctx context.Context, serverID uuid.UUID, hostname, internalIP string) (queries.Server, error) {
	f.allocateCalls++
	return f.allocateAndRegister, f.allocateErr
}

func (f *fakeServerRegistrationStore) EnsureServerSettings(ctx context.Context, serverID uuid.UUID) error {
	f.ensureCalls++
	return f.ensureErr
}

func TestRegisterServer_ReusesExistingIPRange(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	ipRange := netip.MustParsePrefix("10.0.0.0/20")
	store := &fakeServerRegistrationStore{
		found: true,
		findServer: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: ipRange,
		},
		reRegister: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: ipRange,
		},
	}

	got, err := registerServer(context.Background(), store, serverID, "host-1", "10.1.1.10")
	if err != nil {
		t.Fatalf("registerServer: %v", err)
	}
	if got.IpRange != ipRange {
		t.Fatalf("registered ip_range = %s, want %s", got.IpRange, ipRange)
	}
	if store.allocateCalls != 0 {
		t.Fatalf("allocateCalls = %d, want 0", store.allocateCalls)
	}
	if store.reuseCalls != 1 {
		t.Fatalf("reuseCalls = %d, want 1", store.reuseCalls)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("ensureCalls = %d, want 1", store.ensureCalls)
	}
}

func TestRegisterServer_AllocatesForNewServer(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	ipRange := netip.MustParsePrefix("10.0.16.0/20")
	store := &fakeServerRegistrationStore{
		allocateAndRegister: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: ipRange,
		},
	}

	got, err := registerServer(context.Background(), store, serverID, "host-2", "10.1.1.11")
	if err != nil {
		t.Fatalf("registerServer: %v", err)
	}
	if got.IpRange != ipRange {
		t.Fatalf("registered ip_range = %s, want %s", got.IpRange, ipRange)
	}
	if store.allocateCalls != 1 {
		t.Fatalf("allocateCalls = %d, want 1", store.allocateCalls)
	}
	if store.reuseCalls != 0 {
		t.Fatalf("reuseCalls = %d, want 0", store.reuseCalls)
	}
	if store.ensureCalls != 1 {
		t.Fatalf("ensureCalls = %d, want 1", store.ensureCalls)
	}
}

func TestRegisterServer_ErrorsWhenEnsureSettingsFails(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{
		allocateAndRegister: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: netip.MustParsePrefix("10.0.16.0/20"),
		},
		ensureErr: errors.New("boom"),
	}

	if _, err := registerServer(context.Background(), store, serverID, "host-3", "10.1.1.12"); err == nil {
		t.Fatal("expected EnsureServerSettings failure")
	}
}

func TestRegisterServer_RejectsLoopbackJoinForExistingCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		internalIP string
	}{
		{name: "ipv4 loopback", internalIP: "127.0.0.1"},
		{name: "ipv6 loopback in brackets", internalIP: "[::1]"},
		{name: "localhost dot", internalIP: "localhost."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			serverID := uuid.New()
			store := &fakeServerRegistrationStore{
				otherServerCount: 1,
			}

			if _, err := registerServer(context.Background(), store, serverID, "host-4", tt.internalIP); err == nil {
				t.Fatalf("expected loopback internal IP %q to be rejected for multi-server join", tt.internalIP)
			}
		})
	}
}

func TestRegisterServer_RejectsLoopbackReregisterForExistingCluster(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{
		found:            true,
		otherServerCount: 1,
		findServer: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: netip.MustParsePrefix("10.0.0.0/20"),
		},
	}

	if _, err := registerServer(context.Background(), store, serverID, "host-4", "[::1]"); err == nil {
		t.Fatal("expected loopback internal IP to be rejected for multi-server re-register")
	}
}

func TestRegisterServer_AllowsLoopbackOnFirstServer(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{
		allocateAndRegister: queries.Server{
			ID:      pgtype.UUID{Bytes: serverID, Valid: true},
			IpRange: netip.MustParsePrefix("10.0.0.0/20"),
		},
	}

	if _, err := registerServer(context.Background(), store, serverID, "host-5", "127.0.0.1"); err != nil {
		t.Fatalf("first server should allow loopback internal IP: %v", err)
	}
}

func TestValidateSharedDatabaseEntryPoint_RejectsLoopbackForExistingCluster(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{otherServerCount: 1}

	if err := validateSharedDatabaseEntryPoint(context.Background(), store, serverID, "postgres://kindling:secret@127.0.0.1:6432/kindling?sslmode=require"); err == nil {
		t.Fatal("expected loopback database host to be rejected for multi-server cluster")
	}
}

func TestValidateSharedDatabaseEntryPoint_AllowsPrivateHostForExistingCluster(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{otherServerCount: 1}

	if err := validateSharedDatabaseEntryPoint(context.Background(), store, serverID, "postgres://kindling:secret@10.50.0.5:6432/kindling?sslmode=require"); err != nil {
		t.Fatalf("expected private database host to be allowed: %v", err)
	}
}

func TestValidateSharedDatabaseEntryPoint_AllowsLoopbackForFirstServer(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	store := &fakeServerRegistrationStore{}

	if err := validateSharedDatabaseEntryPoint(context.Background(), store, serverID, "postgres://kindling:secret@127.0.0.1:6432/kindling?sslmode=require"); err != nil {
		t.Fatalf("first server should allow loopback database host: %v", err)
	}
}
