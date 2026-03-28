package deploy

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
)

func TestEffectiveReplicaCount(t *testing.T) {
	wake := pgtype.Timestamptz{Valid: true, Time: time.Now()}
	proj := queries.Project{
		DesiredInstanceCount: 3,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
		ScaledToZero:         false,
	}
	dep := queries.Deployment{}
	if got := effectiveReplicaCount(proj, dep); got != 3 {
		t.Fatalf("got %d want 3", got)
	}
	proj.ScaledToZero = true
	if got := effectiveReplicaCount(proj, dep); got != 0 {
		t.Fatalf("scaled_to_zero: got %d want 0", got)
	}
	dep.WakeRequestedAt = wake
	if got := effectiveReplicaCount(proj, dep); got != 1 {
		t.Fatalf("wake + scaled_to_zero: got %d want 1", got)
	}
	proj.ScaledToZero = false
	proj.MinInstanceCount = 2
	proj.DesiredInstanceCount = 0
	if got := effectiveReplicaCount(proj, dep); got != 2 {
		t.Fatalf("wake + desired 0: got %d want 2", got)
	}
	dep.WakeRequestedAt = pgtype.Timestamptz{}
	if got := effectiveReplicaCount(proj, dep); got != 2 {
		t.Fatalf("desired 0: got %d want 2", got)
	}
	proj.MaxInstanceCount = 0
	if got := effectiveReplicaCount(proj, dep); got != 0 {
		t.Fatalf("max 0: got %d want 0", got)
	}
}

func TestRequiresExternalHealthCheck(t *testing.T) {
	if requiresExternalHealthCheck("apple-vz") {
		t.Fatal("expected apple-vz to rely on runtime readiness instead of external health checks")
	}

	if !requiresExternalHealthCheck("crun") {
		t.Fatal("expected crun runtime to keep external health checks")
	}
}

func TestShouldKeepRunningVM(t *testing.T) {
	vm := queries.Vm{
		Status: "running",
	}

	if shouldKeepRunningVM(vm, "cloud-hypervisor", false) {
		t.Fatal("expected unhealthy external runtime VM to be recycled")
	}

	if !shouldKeepRunningVM(vm, "cloud-hypervisor", true) {
		t.Fatal("expected healthy external runtime VM to stay in service")
	}

	if !shouldKeepRunningVM(vm, "apple-vz", false) {
		t.Fatal("expected apple-vz VM to rely on runtime readiness instead of host health checks")
	}

	vm.DeletedAt = pgtype.Timestamptz{Valid: true, Time: time.Now()}
	if shouldKeepRunningVM(vm, "cloud-hypervisor", true) {
		t.Fatal("expected deleted VM to be recycled")
	}
}

func TestSelectLaunchModePrefersResumeThenCloneThenCold(t *testing.T) {
	t.Parallel()

	templateID := "tmpl-1"

	tests := []struct {
		name       string
		inst       queries.DeploymentInstance
		vm         queries.Vm
		template   string
		wantMode   launchMode
		wantLaunch bool
	}{
		{
			name: "resume suspended warm pool first",
			inst: queries.DeploymentInstance{
				Role:   deploymentInstanceRoleWarmPool,
				Status: vmStatusSuspended,
				VmID:   pgtype.UUID{Bytes: uuid.New(), Valid: true},
			},
			vm: queries.Vm{
				Status: vmStatusSuspended,
			},
			template:   templateID,
			wantMode:   launchModeResume,
			wantLaunch: true,
		},
		{
			name: "clone from template when no suspended warm pool exists",
			inst: queries.DeploymentInstance{
				Role: deploymentInstanceRoleActive,
			},
			template:   templateID,
			wantMode:   launchModeClone,
			wantLaunch: true,
		},
		{
			name: "cold start when neither resume nor clone is available",
			inst: queries.DeploymentInstance{
				Role: deploymentInstanceRoleActive,
			},
			wantMode:   launchModeCold,
			wantLaunch: true,
		},
		{
			name: "no work for non active role",
			inst: queries.DeploymentInstance{
				Role: deploymentInstanceRoleTemplate,
			},
			template: templateID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotMode, ok := selectLaunchMode(tt.inst, tt.vm, tt.template)
			if ok != tt.wantLaunch {
				t.Fatalf("ok = %v, want %v", ok, tt.wantLaunch)
			}
			if gotMode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", gotMode, tt.wantMode)
			}
		})
	}
}

func TestPersistentVolumeMountFromRow(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	got := persistentVolumeMountFromRow(queries.ProjectVolume{
		ID:         pgtype.UUID{Bytes: id, Valid: true},
		MountPath:  "/data",
		SizeGb:     12,
		Filesystem: "ext4",
	})

	if got == nil {
		t.Fatal("expected mount")
	}
	if got.ID != id {
		t.Fatalf("id = %s, want %s", got.ID, id)
	}
	if got.HostPath != runtime.PersistentVolumePath(id) {
		t.Fatalf("host path = %q, want %q", got.HostPath, runtime.PersistentVolumePath(id))
	}
	if got.MountPath != "/data" {
		t.Fatalf("mount path = %q", got.MountPath)
	}
	if got.SizeGB != 12 {
		t.Fatalf("size_gb = %d", got.SizeGB)
	}
}
