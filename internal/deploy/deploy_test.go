package deploy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
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
	if got := effectiveReplicaCount(proj, nil, dep); got != 3 {
		t.Fatalf("got %d want 3", got)
	}
	proj.ScaledToZero = true
	if got := effectiveReplicaCount(proj, nil, dep); got != 0 {
		t.Fatalf("scaled_to_zero: got %d want 0", got)
	}
	dep.WakeRequestedAt = wake
	if got := effectiveReplicaCount(proj, nil, dep); got != 3 {
		t.Fatalf("wake + scaled_to_zero: got %d want 3 (full project target)", got)
	}
	proj.ScaledToZero = false
	proj.MinInstanceCount = 2
	proj.DesiredInstanceCount = 0
	if got := effectiveReplicaCount(proj, nil, dep); got != 2 {
		t.Fatalf("wake + desired 0: got %d want 2", got)
	}
	dep.WakeRequestedAt = pgtype.Timestamptz{}
	if got := effectiveReplicaCount(proj, nil, dep); got != 2 {
		t.Fatalf("desired 0: got %d want 2", got)
	}
	proj.MaxInstanceCount = 0
	if got := effectiveReplicaCount(proj, nil, dep); got != 0 {
		t.Fatalf("max 0: got %d want 0", got)
	}
}

func TestEffectiveReplicaCountWakeConvergesToServiceTarget(t *testing.T) {
	wake := pgtype.Timestamptz{Valid: true, Time: time.Now()}
	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}
	svc := &queries.Service{DesiredInstanceCount: 4}
	dep := queries.Deployment{WakeRequestedAt: wake}
	if got := effectiveReplicaCount(proj, svc, dep); got != 4 {
		t.Fatalf("wake: got %d want 4", got)
	}
}

func TestEffectiveReplicaCountUsesServiceDesiredCount(t *testing.T) {
	proj := queries.Project{
		DesiredInstanceCount: 1,
		MinInstanceCount:     1,
		MaxInstanceCount:     5,
	}
	service := &queries.Service{DesiredInstanceCount: 4}
	if got := effectiveReplicaCount(proj, service, queries.Deployment{}); got != 4 {
		t.Fatalf("got %d want 4", got)
	}
}

func TestCountProvisionableInstancesIncludesWarmPool(t *testing.T) {
	t.Parallel()

	d := &Deployer{}
	instList := []queries.DeploymentInstance{
		{Role: deploymentInstanceRoleActive},
		{Role: deploymentInstanceRoleWarmPool},
	}

	if got := d.countProvisionableInstances(instList); got != 2 {
		t.Fatalf("got %d want active + warm_pool provisionable instances", got)
	}
}

func TestWarmPoolExtraRetentionEligible(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{
			name: "recent request",
			last: now.Add(-5 * time.Minute),
			want: true,
		},
		{
			name: "expired request",
			last: now.Add(-20 * time.Minute),
			want: false,
		},
		{
			name: "future timestamp does not count as recent traffic",
			last: now.Add(2 * time.Minute),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := warmPoolExtraRetentionEligible(tt.last, now); got != tt.want {
				t.Fatalf("warmPoolExtraRetentionEligible(%v, %v) = %v want %v", tt.last, now, got, tt.want)
			}
		})
	}
}

func TestWarmPoolPromotionCandidates(t *testing.T) {
	t.Parallel()

	firstWarm := pguuid.ToPgtype(uuid.MustParse("11111111-1111-1111-1111-111111111111"))
	secondWarm := pguuid.ToPgtype(uuid.MustParse("22222222-2222-2222-2222-222222222222"))
	activeID := pguuid.ToPgtype(uuid.MustParse("33333333-3333-3333-3333-333333333333"))

	tests := []struct {
		name    string
		desired int
		insts   []queries.DeploymentInstance
		want    []pgtype.UUID
	}{
		{
			name:    "no deficit leaves warm pool dormant",
			desired: 1,
			insts: []queries.DeploymentInstance{
				{ID: activeID, Role: deploymentInstanceRoleActive},
				{ID: firstWarm, Role: deploymentInstanceRoleWarmPool},
				{ID: secondWarm, Role: deploymentInstanceRoleWarmPool},
			},
			want: nil,
		},
		{
			name:    "promotes oldest warm pool when active deficit exists",
			desired: 1,
			insts: []queries.DeploymentInstance{
				{ID: firstWarm, Role: deploymentInstanceRoleWarmPool},
				{ID: secondWarm, Role: deploymentInstanceRoleWarmPool},
			},
			want: []pgtype.UUID{firstWarm},
		},
		{
			name:    "promotes only deficit count",
			desired: 2,
			insts: []queries.DeploymentInstance{
				{ID: activeID, Role: deploymentInstanceRoleActive},
				{ID: firstWarm, Role: deploymentInstanceRoleWarmPool},
				{ID: secondWarm, Role: deploymentInstanceRoleWarmPool},
			},
			want: []pgtype.UUID{firstWarm},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := warmPoolPromotionCandidates(tt.insts, tt.desired)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d candidates want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("candidate[%d] = %v want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSuspendedResumeRequiresActiveRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		inst queries.DeploymentInstance
		want bool
	}{
		{
			name: "active instance resumes",
			inst: queries.DeploymentInstance{Role: deploymentInstanceRoleActive},
			want: true,
		},
		{
			name: "legacy empty role resumes",
			inst: queries.DeploymentInstance{},
			want: true,
		},
		{
			name: "warm pool stays dormant until promoted",
			inst: queries.DeploymentInstance{Role: deploymentInstanceRoleWarmPool},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := suspendedResumeEligible(tt.inst); got != tt.want {
				t.Fatalf("suspendedResumeEligible(%q) = %v want %v", tt.inst.Role, got, tt.want)
			}
		})
	}
}

func TestStartNewInstanceSkipsStoppedWarmPoolWithoutTouchingDB(t *testing.T) {
	t.Parallel()

	d := &Deployer{
		q: queries.New(fakeUnexpectedDBTX{}),
	}
	err := d.startNewInstance(
		context.Background(),
		queries.Deployment{},
		queries.DeploymentInstance{
			ID:   pgtype.UUID{Bytes: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), Valid: true},
			Role: deploymentInstanceRoleWarmPool,
			Status: "stopped",
		},
		"kindling/peppermint-lab/kindling:test",
		nil,
		"",
		pgtype.UUID{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("startNewInstance() error = %v, want nil for warm_pool no-op", err)
	}
}

func TestStartNewInstanceNormalizesWarmPoolStartingBackToStopped(t *testing.T) {
	t.Parallel()

	inst := queries.DeploymentInstance{
		ID:           pgtype.UUID{Bytes: uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"), Valid: true},
		DeploymentID: pgtype.UUID{Bytes: uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"), Valid: true},
		Role:         deploymentInstanceRoleWarmPool,
		Status:       "starting",
	}
	db := &fakeDeploymentInstanceStatusDBTX{inst: inst}
	d := &Deployer{
		q: queries.New(db),
	}
	if err := d.startNewInstance(
		context.Background(),
		queries.Deployment{},
		inst,
		"kindling/peppermint-lab/kindling:test",
		nil,
		"",
		pgtype.UUID{},
		nil,
		nil,
	); err != nil {
		t.Fatalf("startNewInstance() error = %v, want nil", err)
	}
	if !db.called {
		t.Fatal("expected warm_pool starting instance to be normalized to stopped")
	}
	if db.status != "stopped" {
		t.Fatalf("updated status = %q want stopped", db.status)
	}
}

func TestRestartBudgetExceededSkipsHealthyRunningInstance(t *testing.T) {
	t.Parallel()

	inst := queries.DeploymentInstance{
		Status:       "running",
		RestartCount: maxRestartCount,
		LastRestartAt: pgtype.Timestamptz{
			Valid: true,
			Time:  time.Now(),
		},
	}

	if restartBudgetExceeded(inst, true, time.Now()) {
		t.Fatal("expected healthy running instance not to trip the restart budget")
	}
}

func TestRestartBudgetExceededTripsRecentUnhealthyInstance(t *testing.T) {
	t.Parallel()

	inst := queries.DeploymentInstance{
		Status:       "failed",
		RestartCount: maxRestartCount,
		LastRestartAt: pgtype.Timestamptz{
			Valid: true,
			Time:  time.Now(),
		},
	}

	if !restartBudgetExceeded(inst, false, time.Now()) {
		t.Fatal("expected unhealthy instance with exhausted budget to trip the circuit breaker")
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

	if !shouldKeepRunningVM(vm, "cloud-hypervisor", false) {
		t.Fatal("expected running cloud-hypervisor VM to stay in service despite a transient host health probe failure")
	}

	if shouldKeepRunningVM(vm, "crun", false) {
		t.Fatal("expected unhealthy crun VM to be recycled")
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

func TestShouldTreatRunningInstanceAsHealthy(t *testing.T) {
	t.Parallel()

	vm := queries.Vm{Status: "running"}
	localInst := queries.DeploymentInstance{
		Role:     deploymentInstanceRoleActive,
		ServerID: pguuid.ToPgtype(uuid.MustParse("11111111-1111-1111-1111-111111111111")),
	}
	remoteInst := queries.DeploymentInstance{
		Role:     deploymentInstanceRoleActive,
		ServerID: pguuid.ToPgtype(uuid.MustParse("22222222-2222-2222-2222-222222222222")),
	}
	localServerID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	if shouldTreatRunningInstanceAsHealthy(localInst, vm, "cloud-hypervisor", false, localServerID, false) {
		t.Fatal("expected local cloud-hypervisor instance with exited runtime to be unhealthy")
	}
	if !shouldTreatRunningInstanceAsHealthy(localInst, vm, "cloud-hypervisor", false, localServerID, true) {
		t.Fatal("expected local cloud-hypervisor instance with live runtime to stay healthy despite transient host probe failure")
	}
	if !shouldTreatRunningInstanceAsHealthy(remoteInst, vm, "cloud-hypervisor", false, localServerID, false) {
		t.Fatal("expected remote cloud-hypervisor instance to rely on database + external signals, not local runtime state")
	}
	if shouldTreatRunningInstanceAsHealthy(localInst, vm, "crun", false, localServerID, true) {
		t.Fatal("expected unhealthy crun instance to fail host health check")
	}
}

func TestHealthCheckRetriesTransientConnectionResets(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("expected hijacker")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}

	d := &Deployer{}
	if !d.healthCheck(host, port) {
		t.Fatal("expected health check to tolerate transient connection resets")
	}
	if got := attempts.Load(); got < 3 {
		t.Fatalf("got %d attempts want at least 3", got)
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

type fakeUnexpectedDBTX struct{}

func (fakeUnexpectedDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (fakeUnexpectedDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (fakeUnexpectedDBTX) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	return errRow{err: errors.New("unexpected QueryRow")}
}

type errRow struct {
	err error
}

func (r errRow) Scan(...any) error { return r.err }

type fakeDeploymentInstanceStatusDBTX struct {
	inst   queries.DeploymentInstance
	called bool
	status string
}

func (f *fakeDeploymentInstanceStatusDBTX) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (f *fakeDeploymentInstanceStatusDBTX) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *fakeDeploymentInstanceStatusDBTX) QueryRow(_ context.Context, _ string, args ...interface{}) pgx.Row {
	f.called = true
	if len(args) >= 2 {
		if status, ok := args[1].(string); ok {
			f.status = status
		}
	}
	updated := f.inst
	updated.Status = f.status
	return deploymentInstanceRow{inst: updated}
}

type deploymentInstanceRow struct {
	inst queries.DeploymentInstance
}

func (r deploymentInstanceRow) Scan(dest ...any) error {
	values := []any{
		r.inst.ID,
		r.inst.DeploymentID,
		r.inst.ServerID,
		r.inst.VmID,
		r.inst.Role,
		r.inst.CloneSourceInstanceID,
		r.inst.Status,
		r.inst.RestartCount,
		r.inst.LastRestartAt,
		r.inst.DeletedAt,
		r.inst.CreatedAt,
		r.inst.UpdatedAt,
	}
	for i, d := range dest {
		switch out := d.(type) {
		case *pgtype.UUID:
			*out = values[i].(pgtype.UUID)
		case *string:
			*out = values[i].(string)
		case *int32:
			*out = values[i].(int32)
		case *pgtype.Timestamptz:
			*out = values[i].(pgtype.Timestamptz)
		default:
			return errors.New("unexpected scan type")
		}
	}
	return nil
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
