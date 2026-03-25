package deploy

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestParseRuntimeAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantIP  netip.Addr
		wantPort int
		wantErr bool
	}{
		{
			name:     "docker host port",
			raw:      "127.0.0.1:32768",
			wantIP:   netip.MustParseAddr("127.0.0.1"),
			wantPort: 32768,
		},
		{
			name:     "apple vz address",
			raw:      "192.168.64.2:3000",
			wantIP:   netip.MustParseAddr("192.168.64.2"),
			wantPort: 3000,
		},
		{
			name:     "full url",
			raw:      "http://127.0.0.1:32768",
			wantIP:   netip.MustParseAddr("127.0.0.1"),
			wantPort: 32768,
		},
		{
			name:     "ipv6 host",
			raw:      "http://[2001:db8::1]:3000",
			wantIP:   netip.MustParseAddr("2001:db8::1"),
			wantPort: 3000,
		},
		{
			name:    "malformed address",
			raw:     "not an address",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotIP, gotPort, err := parseRuntimeAddress(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRuntimeAddress(%q): %v", tt.raw, err)
			}
			if gotIP != tt.wantIP {
				t.Fatalf("ip = %s, want %s", gotIP, tt.wantIP)
			}
			if gotPort != tt.wantPort {
				t.Fatalf("port = %d, want %d", gotPort, tt.wantPort)
			}
		})
	}
}

func TestPersistRuntimeVMMetadata(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	deploymentID := uuid.New()
	imageID := uuid.New()

	store := &fakeRuntimeVMMetadataStore{
		updateResult: queries.Deployment{
			ID:    pgUUID(deploymentID),
			VmID:  pgUUID(uuid.New()),
		},
	}

	d := &Deployer{serverID: serverID}
	dep := queries.Deployment{
		ID:      pgUUID(deploymentID),
		ImageID: pgUUID(imageID),
	}

	gotDep, err := d.persistRuntimeVMMetadata(
		context.Background(),
		store,
		dep,
		"127.0.0.1:32768",
		1,
		512,
		[]string{"PORT=3000", "HELLO=world"},
	)
	if err != nil {
		t.Fatalf("persistRuntimeVMMetadata: %v", err)
	}

	if !store.created {
		t.Fatal("expected VMCreate to be called")
	}
	if !store.updated {
		t.Fatal("expected DeploymentUpdateVM to be called")
	}
	if store.createArg.ServerID != pgUUID(serverID) {
		t.Fatalf("server_id = %+v, want %+v", store.createArg.ServerID, pgUUID(serverID))
	}
	if store.createArg.ImageID != pgUUID(imageID) {
		t.Fatalf("image_id = %+v, want %+v", store.createArg.ImageID, pgUUID(imageID))
	}
	if store.createArg.Status != "running" {
		t.Fatalf("status = %q, want running", store.createArg.Status)
	}
	if store.createArg.IpAddress != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("ip_address = %s", store.createArg.IpAddress)
	}
	if !store.createArg.Port.Valid || store.createArg.Port.Int32 != 32768 {
		t.Fatalf("port = %+v, want 32768", store.createArg.Port)
	}
	if store.updateArg.ID != pgUUID(deploymentID) {
		t.Fatalf("deployment id = %+v, want %+v", store.updateArg.ID, pgUUID(deploymentID))
	}
	if !store.updateArg.VmID.Valid {
		t.Fatal("expected vm id to be set on deployment update")
	}
	if gotDep.VmID != store.updateArg.VmID {
		t.Fatalf("returned deployment vm_id = %+v, want %+v", gotDep.VmID, store.updateArg.VmID)
	}

	var persistedEnv []string
	if err := json.Unmarshal([]byte(store.createArg.EnvVariables.String), &persistedEnv); err != nil {
		t.Fatalf("env_variables should be JSON: %v", err)
	}
	if len(persistedEnv) != 2 || persistedEnv[1] != "HELLO=world" {
		t.Fatalf("persisted env = %#v", persistedEnv)
	}
}

func TestPersistRuntimeVMMetadataSoftDeletesVMWhenAttachFails(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	deploymentID := uuid.New()
	imageID := uuid.New()

	store := &fakeRuntimeVMMetadataStore{
		updateErr: assertErr("attach failed"),
	}

	d := &Deployer{serverID: serverID}
	dep := queries.Deployment{
		ID:      pgUUID(deploymentID),
		ImageID: pgUUID(imageID),
	}

	_, err := d.persistRuntimeVMMetadata(
		context.Background(),
		store,
		dep,
		"127.0.0.1:32768",
		1,
		512,
		nil,
	)
	if err == nil {
		t.Fatal("expected attach error")
	}
	if !store.softDeleted {
		t.Fatal("expected VMSoftDelete to be called when attach fails")
	}
	if !store.softDeleteID.Valid {
		t.Fatal("expected soft delete vm id to be recorded")
	}
}

type fakeRuntimeVMMetadataStore struct {
	createArg    queries.VMCreateParams
	updateArg    queries.DeploymentUpdateVMParams
	updateResult queries.Deployment
	updateErr    error
	softDeleteID pgtype.UUID
	created      bool
	updated      bool
	softDeleted  bool
}

func (f *fakeRuntimeVMMetadataStore) VMCreate(_ context.Context, arg queries.VMCreateParams) (queries.Vm, error) {
	f.created = true
	f.createArg = arg
	return queries.Vm{ID: arg.ID}, nil
}

func (f *fakeRuntimeVMMetadataStore) DeploymentUpdateVM(_ context.Context, arg queries.DeploymentUpdateVMParams) (queries.Deployment, error) {
	f.updated = true
	f.updateArg = arg
	if f.updateErr != nil {
		return queries.Deployment{}, f.updateErr
	}
	f.updateResult.VmID = arg.VmID
	return f.updateResult, nil
}

func (f *fakeRuntimeVMMetadataStore) VMSoftDelete(_ context.Context, id pgtype.UUID) error {
	f.softDeleted = true
	f.softDeleteID = id
	return nil
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func assertErr(msg string) error {
	return &testErr{msg: msg}
}

type testErr struct {
	msg string
}

func (e *testErr) Error() string {
	return e.msg
}
