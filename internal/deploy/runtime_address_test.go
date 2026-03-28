package deploy

import (
	"context"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func TestParseRuntimeAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantIP   netip.Addr
		wantPort int
		wantErr  bool
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

func TestPersistInstanceVMMetadata(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	instanceID := uuid.New()
	imageID := uuid.New()

	store := &fakeInstanceVMStore{
		instance: queries.DeploymentInstance{
			ID:           pguuid.ToPgtype(instanceID),
			DeploymentID: pguuid.ToPgtype(uuid.New()),
			Status:       "starting",
		},
	}

	d := &Deployer{serverID: serverID}
	vmID, err := d.persistInstanceVMMetadata(
		context.Background(),
		store,
		pguuid.ToPgtype(instanceID),
		pguuid.ToPgtype(imageID),
		serverID,
		"starting",
		"starting",
		"127.0.0.1:32768",
		1,
		512,
		[]string{"PORT=3000", "HELLO=world"},
		instanceVMMetadata{
			Runtime:     "cloud-hypervisor",
			SnapshotRef: "tmpl://deployment/rootfs",
		},
	)
	if err != nil {
		t.Fatalf("persistInstanceVMMetadata: %v", err)
	}
	if vmID == uuid.Nil {
		t.Fatal("expected VM id")
	}

	if !store.created {
		t.Fatal("expected VMCreate to be called")
	}
	if !store.attached {
		t.Fatal("expected DeploymentInstanceAttachVM to be called")
	}
	if store.createArg.ServerID != pguuid.ToPgtype(serverID) {
		t.Fatalf("server_id = %+v, want %+v", store.createArg.ServerID, pguuid.ToPgtype(serverID))
	}
	if store.createArg.ImageID != pguuid.ToPgtype(imageID) {
		t.Fatalf("image_id = %+v, want %+v", store.createArg.ImageID, pguuid.ToPgtype(imageID))
	}
	if store.createArg.Status != "starting" {
		t.Fatalf("status = %q, want starting", store.createArg.Status)
	}
	if store.createArg.Runtime != "cloud-hypervisor" {
		t.Fatalf("runtime = %q, want cloud-hypervisor", store.createArg.Runtime)
	}
	if !store.createArg.SnapshotRef.Valid || store.createArg.SnapshotRef.String != "tmpl://deployment/rootfs" {
		t.Fatalf("snapshot_ref = %+v, want tmpl://deployment/rootfs", store.createArg.SnapshotRef)
	}
	if store.createArg.CloneSourceVmID.Valid {
		t.Fatalf("clone_source_vm_id = %+v, want invalid", store.createArg.CloneSourceVmID)
	}
	if store.attachArg.Status != "starting" {
		t.Fatalf("attach status = %q, want starting", store.attachArg.Status)
	}
	if store.attachArg.ID != pguuid.ToPgtype(instanceID) {
		t.Fatalf("attach instance id mismatch")
	}

	var persistedEnv []string
	if err := json.Unmarshal([]byte(store.createArg.EnvVariables.String), &persistedEnv); err != nil {
		t.Fatalf("env_variables should be JSON: %v", err)
	}
	if len(persistedEnv) != 2 || persistedEnv[1] != "HELLO=world" {
		t.Fatalf("persisted env = %#v", persistedEnv)
	}
}

func TestPersistInstanceVMMetadataSoftDeletesVMWhenAttachFails(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	instanceID := uuid.New()
	imageID := uuid.New()

	store := &fakeInstanceVMStore{
		instance: queries.DeploymentInstance{
			ID:     pguuid.ToPgtype(instanceID),
			Status: "starting",
		},
		attachErr: assertErr("attach failed"),
	}

	d := &Deployer{serverID: serverID}
	_, err := d.persistInstanceVMMetadata(
		context.Background(),
		store,
		pguuid.ToPgtype(instanceID),
		pguuid.ToPgtype(imageID),
		serverID,
		"starting",
		"starting",
		"127.0.0.1:32768",
		1,
		512,
		nil,
		instanceVMMetadata{},
	)
	if err == nil {
		t.Fatal("expected attach error")
	}
	if !store.softDeleted {
		t.Fatal("expected VMSoftDelete to be called when attach fails")
	}
}

func TestPersistInstanceVMMetadataStoresCloneLineage(t *testing.T) {
	t.Parallel()

	serverID := uuid.New()
	instanceID := uuid.New()
	imageID := uuid.New()
	parentVMID := uuid.New()

	store := &fakeInstanceVMStore{
		instance: queries.DeploymentInstance{
			ID:           pguuid.ToPgtype(instanceID),
			DeploymentID: pguuid.ToPgtype(uuid.New()),
			Status:       "starting",
		},
	}

	d := &Deployer{serverID: serverID}
	_, err := d.persistInstanceVMMetadata(
		context.Background(),
		store,
		pguuid.ToPgtype(instanceID),
		pguuid.ToPgtype(imageID),
		serverID,
		"starting",
		"starting",
		"127.0.0.1:32768",
		1,
		512,
		nil,
		instanceVMMetadata{
			Runtime:         "cloud-hypervisor",
			SnapshotRef:     "tmpl://clone/rootfs",
			CloneSourceVMID: pguuid.ToPgtype(parentVMID),
		},
	)
	if err != nil {
		t.Fatalf("persistInstanceVMMetadata: %v", err)
	}
	if !store.createArg.CloneSourceVmID.Valid || store.createArg.CloneSourceVmID.Bytes != parentVMID {
		t.Fatalf("clone_source_vm_id = %+v, want %+v", store.createArg.CloneSourceVmID, pguuid.ToPgtype(parentVMID))
	}
}

type fakeInstanceVMStore struct {
	instance    queries.DeploymentInstance
	attachErr   error
	createArg   queries.VMCreateParams
	attachArg   queries.DeploymentInstanceAttachVMParams
	created     bool
	attached    bool
	softDeleted bool
}

func (f *fakeInstanceVMStore) DeploymentInstanceFirstByID(_ context.Context, _ pgtype.UUID) (queries.DeploymentInstance, error) {
	return f.instance, nil
}

func (f *fakeInstanceVMStore) VMCreate(_ context.Context, arg queries.VMCreateParams) (queries.Vm, error) {
	f.created = true
	f.createArg = arg
	return queries.Vm{ID: arg.ID}, nil
}

func (f *fakeInstanceVMStore) DeploymentInstanceAttachVM(_ context.Context, arg queries.DeploymentInstanceAttachVMParams) (queries.DeploymentInstance, error) {
	f.attached = true
	f.attachArg = arg
	if f.attachErr != nil {
		return queries.DeploymentInstance{}, f.attachErr
	}
	return f.instance, nil
}

func (f *fakeInstanceVMStore) VMSoftDelete(_ context.Context, id pgtype.UUID) error {
	f.softDeleted = true
	_ = id
	return nil
}

func assertErr(msg string) error {
	return &testErrMsg{msg: msg}
}

type testErrMsg struct {
	msg string
}

func (e *testErrMsg) Error() string {
	return e.msg
}
