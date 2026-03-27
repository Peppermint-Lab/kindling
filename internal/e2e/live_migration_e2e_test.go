//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/migrationreconcile"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc"
	kindlingruntime "github.com/kindlingvm/kindling/internal/runtime"
)

type liveMigrationHarness struct {
	db       *database.DB
	q        *queries.Queries
	client   *http.Client
	server   *httptest.Server
	sourceID uuid.UUID
	destID   uuid.UUID
	project  uuid.UUID
	deploy   uuid.UUID
	image    uuid.UUID
	instance uuid.UUID
	sourceVM uuid.UUID

	sourceHandler *migrationreconcile.Handler
	destHandler   *migrationreconcile.Handler
	sourceRuntime *fakeLiveMigrationRuntime
	destRuntime   *fakeLiveMigrationRuntime

	scheduleCh chan uuid.UUID

	notifyMu    sync.Mutex
	notifyCount int
}

type liveMigrationHarnessOptions struct {
	sourceVersion string
	destVersion   string
	sourceShared  string
	destShared    string
	initialState  string
	sendErr       error
	finalizeErr   error
}

func newLiveMigrationHarness(t *testing.T, opts liveMigrationHarnessOptions) *liveMigrationHarness {
	t.Helper()

	if strings.TrimSpace(opts.sourceVersion) == "" {
		opts.sourceVersion = "cloud-hypervisor 46.0"
	}
	if strings.TrimSpace(opts.destVersion) == "" {
		opts.destVersion = opts.sourceVersion
	}
	if strings.TrimSpace(opts.sourceShared) == "" {
		opts.sourceShared = "/mnt/kindling-rootfs"
	}
	if strings.TrimSpace(opts.destShared) == "" {
		opts.destShared = opts.sourceShared
	}
	if opts.initialState == "" {
		opts.initialState = "counter=7"
	}

	dsn := e2eDatabaseURL()
	if dsn == "" {
		t.Skip("set KINDLING_E2E_DATABASE_URL or DATABASE_URL to run integration tests")
	}
	ctx := context.Background()
	db, err := database.New(ctx, dsn)
	if err != nil {
		t.Skipf("postgres not reachable: %v", err)
	}
	t.Cleanup(db.Close)
	if err := database.Migrate(ctx, db.Pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	q := queries.New(db.Pool)
	api := rpc.NewAPI(q, nil, nil)
	mux := http.NewServeMux()
	api.Register(mux)
	ts := httptest.NewServer(auth.Middleware(q, mux))
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	base, _ := url.Parse(ts.URL)
	if err := ensureSession(ctx, t, client, base, q); err != nil {
		t.Fatal(err)
	}

	h := &liveMigrationHarness{
		db:         db,
		q:          q,
		client:     client,
		server:     ts,
		sourceID:   uuid.New(),
		destID:     uuid.New(),
		project:    uuid.New(),
		deploy:     uuid.New(),
		image:      uuid.New(),
		instance:   uuid.New(),
		sourceVM:   uuid.New(),
		scheduleCh: make(chan uuid.UUID, 4),
	}

	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Pool.Exec(cctx, `DELETE FROM instance_migrations WHERE deployment_instance_id = $1`, pgUUID(h.instance))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM instance_usage_samples WHERE deployment_instance_id = $1`, pgUUID(h.instance))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM deployment_instances WHERE id = $1`, pgUUID(h.instance))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM deployments WHERE id = $1`, pgUUID(h.deploy))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM vms WHERE image_id = $1 AND (server_id = $2 OR server_id = $3)`, pgUUID(h.image), pgUUID(h.sourceID), pgUUID(h.destID))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM projects WHERE id = $1`, pgUUID(h.project))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM images WHERE id = $1`, pgUUID(h.image))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM server_component_statuses WHERE server_id = $1 OR server_id = $2`, pgUUID(h.sourceID), pgUUID(h.destID))
		_, _ = db.Pool.Exec(cctx, `DELETE FROM servers WHERE id = $1 OR id = $2`, pgUUID(h.sourceID), pgUUID(h.destID))
	})

	if _, err := db.Pool.Exec(ctx, `
INSERT INTO servers (id, hostname, internal_ip, ip_range, status, last_heartbeat_at)
VALUES ($1, 'live-migration-source', '10.250.0.1', '10.250.0.0/20'::cidr, 'active', NOW()),
       ($2, 'live-migration-dest', '10.250.16.1', '10.250.16.0/20'::cidr, 'active', NOW())`,
		pgUUID(h.sourceID),
		pgUUID(h.destID),
	); err != nil {
		t.Fatalf("seed servers: %v", err)
	}

	if err := upsertWorkerStatus(ctx, q, h.sourceID, opts.sourceVersion, opts.sourceShared); err != nil {
		t.Fatalf("seed source worker status: %v", err)
	}
	if err := upsertWorkerStatus(ctx, q, h.destID, opts.destVersion, opts.destShared); err != nil {
		t.Fatalf("seed destination worker status: %v", err)
	}

	if _, err := q.ProjectCreate(ctx, queries.ProjectCreateParams{
		ID:                   pgUUID(h.project),
		OrgID:                auth.PgUUID(auth.BootstrapOrganizationID),
		Name:                 "live-migration-" + h.project.String()[:8],
		GithubRepository:     "",
		GithubInstallationID: 0,
		GithubWebhookSecret:  "",
		RootDirectory:        "/",
		DockerfilePath:       "Dockerfile",
		DesiredInstanceCount: 1,
	}); err != nil {
		t.Fatalf("project create: %v", err)
	}

	if _, err := q.ImageFindOrCreate(ctx, queries.ImageFindOrCreateParams{
		ID:         pgUUID(h.image),
		Registry:   "registry.test",
		Repository: "kindling/live-migration",
		Tag:        h.image.String()[:12],
	}); err != nil {
		t.Fatalf("image create: %v", err)
	}

	if _, err := q.DeploymentCreate(ctx, queries.DeploymentCreateParams{
		ID:                   pgUUID(h.deploy),
		ProjectID:            pgUUID(h.project),
		GithubCommit:         "deadbeef",
		GithubBranch:         "main",
		DeploymentKind:       "production",
		PreviewEnvironmentID: pgtype.UUID{Valid: false},
	}); err != nil {
		t.Fatalf("deployment create: %v", err)
	}
	if err := q.DeploymentMarkRunning(ctx, pgUUID(h.deploy)); err != nil {
		t.Fatalf("deployment mark running: %v", err)
	}

	if _, err := q.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
		ID:           pgUUID(h.instance),
		DeploymentID: pgUUID(h.deploy),
	}); err != nil {
		t.Fatalf("deployment instance create: %v", err)
	}
	if _, err := q.DeploymentInstanceUpdateServer(ctx, queries.DeploymentInstanceUpdateServerParams{
		ID:       pgUUID(h.instance),
		ServerID: pgUUID(h.sourceID),
	}); err != nil {
		t.Fatalf("deployment instance update server: %v", err)
	}

	sharedRootfsRef := sharedRootfsRef(opts.sourceShared, h.instance)
	vm, err := q.VMCreate(ctx, queries.VMCreateParams{
		ID:              pgUUID(h.sourceVM),
		ServerID:        pgUUID(h.sourceID),
		ImageID:         pgUUID(h.image),
		Status:          "running",
		Runtime:         "cloud-hypervisor",
		SnapshotRef:     pgtype.Text{},
		SharedRootfsRef: sharedRootfsRef,
		CloneSourceVmID: pgtype.UUID{},
		Vcpus:           1,
		Memory:          512,
		IpAddress:       mustInet("10.9.0.10"),
		Port:            pgtype.Int4{Int32: 3000, Valid: true},
		EnvVariables:    pgtype.Text{},
	})
	if err != nil {
		t.Fatalf("vm create: %v", err)
	}
	if _, err := q.DeploymentInstanceAttachVM(ctx, queries.DeploymentInstanceAttachVMParams{
		ID:     pgUUID(h.instance),
		VmID:   vm.ID,
		Status: "running",
	}); err != nil {
		t.Fatalf("deployment instance attach vm: %v", err)
	}

	coord := newFakeMigrationCoordinator()
	h.sourceRuntime = newFakeLiveMigrationRuntime("10.250.0.1:3000", opts.sourceVersion, opts.sourceShared, coord)
	h.destRuntime = newFakeLiveMigrationRuntime("10.250.16.1:3000", opts.destVersion, opts.destShared, coord)
	h.sourceRuntime.instances[h.instance] = &fakeLiveMigrationInstance{
		payload:         opts.initialState,
		sharedRootfsRef: sharedRootfsRef,
		runtimeURL:      "10.250.0.1:3000",
	}
	h.sourceRuntime.sendErr = opts.sendErr
	h.destRuntime.finalizeErr = opts.finalizeErr

	sched := reconciler.New(reconciler.Config{
		Name: "live-migration-e2e",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			select {
			case h.scheduleCh <- id:
			default:
			}
			return nil
		},
		DefaultAfter: 24 * time.Hour,
	})
	schedCtx, cancelSched := context.WithCancel(context.Background())
	t.Cleanup(cancelSched)
	go sched.Start(schedCtx)

	notify := func() {
		h.notifyMu.Lock()
		h.notifyCount++
		h.notifyMu.Unlock()
	}
	h.sourceHandler = migrationreconcile.NewHandler(q, db.Pool, h.sourceRuntime, h.sourceID, sched, notify)
	h.destHandler = migrationreconcile.NewHandler(q, db.Pool, h.destRuntime, h.destID, sched, notify)

	return h
}

func TestLiveMigration_HappyPathPreservesStateAndCommits(t *testing.T) {
	h := newLiveMigrationHarness(t, liveMigrationHarnessOptions{})
	mig := h.startLiveMigration(t, http.StatusAccepted)

	ctx := context.Background()
	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination prepare reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "destination_prepared" {
		t.Fatalf("migration state after destination prepare = %q", mig.State)
	}
	if strings.TrimSpace(mig.ReceiveAddr) == "" || !strings.Contains(mig.ReceiveAddr, "10.250.16.1:") {
		t.Fatalf("receive addr = %q", mig.ReceiveAddr)
	}

	if err := h.sourceHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("source send reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "received" {
		t.Fatalf("migration state after source send = %q", mig.State)
	}

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination commit reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "completed" {
		t.Fatalf("migration state after destination commit = %q", mig.State)
	}
	if strings.TrimSpace(mig.DestinationRuntimeUrl) == "" {
		t.Fatal("expected destination runtime url to be recorded")
	}

	inst, err := h.q.DeploymentInstanceFirstByID(ctx, pgUUID(h.instance))
	if err != nil {
		t.Fatalf("deployment instance fetch: %v", err)
	}
	if got := uuid.UUID(inst.ServerID.Bytes); got != h.destID {
		t.Fatalf("deployment instance server = %s, want %s", got, h.destID)
	}
	if !inst.VmID.Valid {
		t.Fatal("expected deployment instance vm id to be updated")
	}

	sourceVM, err := h.q.VMFirstByID(ctx, pgUUID(h.sourceVM))
	if err != nil {
		t.Fatalf("source vm fetch: %v", err)
	}
	if !sourceVM.DeletedAt.Valid {
		t.Fatal("expected source vm to be soft-deleted after migration")
	}

	destPayload, ok := h.destRuntime.payload(h.instance)
	if !ok {
		t.Fatal("expected destination runtime to hold migrated state")
	}
	if destPayload != "counter=7" {
		t.Fatalf("destination payload = %q, want counter=7", destPayload)
	}

	respBody := h.getMigration(t)
	var out struct {
		Migration struct {
			State                 string `json:"state"`
			DestinationRuntimeURL string `json:"destination_runtime_url"`
		} `json:"migration"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("get migration json: %v", err)
	}
	if out.Migration.State != "completed" {
		t.Fatalf("GET migration state = %q", out.Migration.State)
	}
	if out.Migration.DestinationRuntimeURL == "" {
		t.Fatal("GET migration missing destination runtime url")
	}
	if h.routeNotifications() == 0 {
		t.Fatal("expected route notifications after successful migration")
	}
}

func TestLiveMigration_APIRejectsIncompatibleDestinations(t *testing.T) {
	t.Run("version mismatch", func(t *testing.T) {
		h := newLiveMigrationHarness(t, liveMigrationHarnessOptions{
			sourceVersion: "cloud-hypervisor 46.0",
			destVersion:   "cloud-hypervisor 45.0",
		})
		body := h.startLiveMigrationExpectError(t, http.StatusConflict)
		if !strings.Contains(string(body), "destination") {
			t.Fatalf("expected destination compatibility error, got %q", body)
		}
		if _, err := h.q.InstanceMigrationLatestByDeploymentInstanceID(context.Background(), pgUUID(h.instance)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expected no migration row on preflight failure, got err=%v", err)
		}
	})

	t.Run("missing shared storage", func(t *testing.T) {
		h := newLiveMigrationHarness(t, liveMigrationHarnessOptions{
			destShared: "/mnt/kindling-rootfs",
		})
		if err := upsertWorkerStatus(context.Background(), h.q, h.destID, "cloud-hypervisor 46.0", ""); err != nil {
			t.Fatalf("clear destination shared rootfs: %v", err)
		}
		body := h.startLiveMigrationExpectError(t, http.StatusConflict)
		if !strings.Contains(string(body), "destination") {
			t.Fatalf("expected destination compatibility error, got %q", body)
		}
		if _, err := h.q.InstanceMigrationLatestByDeploymentInstanceID(context.Background(), pgUUID(h.instance)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expected no migration row on preflight failure, got err=%v", err)
		}
	})
}

func TestLiveMigration_SendFailureLeavesSourceServing(t *testing.T) {
	h := newLiveMigrationHarness(t, liveMigrationHarnessOptions{
		sendErr: errors.New("simulated send failure"),
	})
	mig := h.startLiveMigration(t, http.StatusAccepted)
	ctx := context.Background()

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination prepare reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "destination_prepared" {
		t.Fatalf("migration state after destination prepare = %q", mig.State)
	}

	if err := h.sourceHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("source send reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "failed" {
		t.Fatalf("migration state after failed send = %q", mig.State)
	}
	if !strings.Contains(mig.FailureMessage, "simulated send failure") {
		t.Fatalf("failure message = %q", mig.FailureMessage)
	}

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination cleanup reconcile: %v", err)
	}
	if _, ok := h.destRuntime.payload(h.instance); ok {
		t.Fatal("destination runtime should not hold migrated state after send failure")
	}
	sourcePayload, ok := h.sourceRuntime.payload(h.instance)
	if !ok || sourcePayload != "counter=7" {
		t.Fatalf("source payload after send failure = %q ok=%v", sourcePayload, ok)
	}
}

func TestLiveMigration_FinalizeFailureFallsBackToEvacuation(t *testing.T) {
	h := newLiveMigrationHarness(t, liveMigrationHarnessOptions{
		finalizeErr: errors.New("simulated finalize failure"),
	})
	mig := h.startLiveMigration(t, http.StatusAccepted)
	ctx := context.Background()

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination prepare reconcile: %v", err)
	}
	if err := h.sourceHandler.Reconcile(ctx, uuid.UUID(h.mustMigration(t).ID.Bytes)); err != nil {
		t.Fatalf("source send reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "received" {
		t.Fatalf("migration state after send = %q", mig.State)
	}

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination finalize reconcile: %v", err)
	}
	mig = h.mustMigration(t)
	if mig.State != "fallback_evacuating" {
		t.Fatalf("migration state after finalize failure = %q", mig.State)
	}
	if !strings.Contains(mig.FailureMessage, "simulated finalize failure") {
		t.Fatalf("fallback failure message = %q", mig.FailureMessage)
	}

	if err := h.destHandler.Reconcile(ctx, uuid.UUID(mig.ID.Bytes)); err != nil {
		t.Fatalf("destination fallback reconcile: %v", err)
	}
	select {
	case scheduled := <-h.scheduleCh:
		if scheduled != h.deploy {
			t.Fatalf("scheduled deployment = %s, want %s", scheduled, h.deploy)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected fallback_evacuating migration to schedule deployment reconcile")
	}

	body := h.startLiveMigrationExpectError(t, http.StatusAccepted)
	if !strings.Contains(string(body), `"state":"pending"`) {
		t.Fatalf("expected a new migration to be allowed after fallback, got %q", body)
	}
}

func (h *liveMigrationHarness) routeNotifications() int {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	return h.notifyCount
}

func (h *liveMigrationHarness) mustMigration(t *testing.T) queries.InstanceMigration {
	t.Helper()
	row, err := h.q.InstanceMigrationLatestByDeploymentInstanceID(context.Background(), pgUUID(h.instance))
	if err != nil {
		t.Fatalf("latest migration: %v", err)
	}
	return row
}

func (h *liveMigrationHarness) startLiveMigration(t *testing.T, wantStatus int) queries.InstanceMigration {
	t.Helper()
	body := h.startLiveMigrationExpectError(t, wantStatus)
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("start live migration json: %v body=%s", err, string(body))
	}
	row, err := h.q.InstanceMigrationFirstByID(context.Background(), pgUUID(uuid.MustParse(out.ID)))
	if err != nil {
		t.Fatalf("migration fetch: %v", err)
	}
	return row
}

func (h *liveMigrationHarness) startLiveMigrationExpectError(t *testing.T, wantStatus int) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/deployment-instances/%s/live-migrate", h.server.URL, h.instance), bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("live migrate status = %d, want %d body=%s", resp.StatusCode, wantStatus, string(body))
	}
	return body
}

func (h *liveMigrationHarness) getMigration(t *testing.T) []byte {
	t.Helper()
	return getOK(t, h.client, fmt.Sprintf("%s/api/deployment-instances/%s/migration", h.server.URL, h.instance))
}

func upsertWorkerStatus(ctx context.Context, q *queries.Queries, serverID uuid.UUID, version, sharedRootfs string) error {
	meta, err := json.Marshal(map[string]any{
		"runtime":                  "cloud-hypervisor",
		"live_migration_enabled":   true,
		"cloud_hypervisor_version": version,
		"shared_rootfs_dir":        sharedRootfs,
	})
	if err != nil {
		return err
	}
	return q.ServerComponentStatusUpsert(ctx, queries.ServerComponentStatusUpsertParams{
		ServerID:         pgUUID(serverID),
		Component:        "worker",
		Status:           "healthy",
		ObservedAt:       pgTS(time.Now()),
		LastSuccessAt:    pgTS(time.Now()),
		LastErrorAt:      pgtype.Timestamptz{},
		LastErrorMessage: "",
		Metadata:         meta,
	})
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func pgTS(v time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: v, Valid: true}
}

func mustInet(raw string) netip.Addr {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		panic(err)
	}
	return addr
}

func sharedRootfsRef(base string, instanceID uuid.UUID) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/rootfs.qcow2", strings.TrimRight(base, "/"), instanceID)
}

type fakeMigrationCoordinator struct {
	mu       sync.Mutex
	prepared map[uuid.UUID]*fakePreparedMigration
}

type fakePreparedMigration struct {
	target      *fakeLiveMigrationRuntime
	receiveAddr string
	payload     string
}

func newFakeMigrationCoordinator() *fakeMigrationCoordinator {
	return &fakeMigrationCoordinator{prepared: make(map[uuid.UUID]*fakePreparedMigration)}
}

type fakeLiveMigrationRuntime struct {
	runtimeURL string
	version    string
	sharedRoot string
	coord      *fakeMigrationCoordinator

	mu           sync.Mutex
	instances    map[uuid.UUID]*fakeLiveMigrationInstance
	sendErr      error
	finalizeErr  error
	prepareCount int
}

type fakeLiveMigrationInstance struct {
	payload         string
	sharedRootfsRef string
	runtimeURL      string
}

func newFakeLiveMigrationRuntime(runtimeURL, version, sharedRoot string, coord *fakeMigrationCoordinator) *fakeLiveMigrationRuntime {
	return &fakeLiveMigrationRuntime{
		runtimeURL: runtimeURL,
		version:    version,
		sharedRoot: sharedRoot,
		coord:      coord,
		instances:  make(map[uuid.UUID]*fakeLiveMigrationInstance),
	}
}

func (r *fakeLiveMigrationRuntime) Name() string { return "cloud-hypervisor" }

func (r *fakeLiveMigrationRuntime) Supports(cap kindlingruntime.Capability) bool {
	return cap == kindlingruntime.CapabilityLiveMigration
}

func (r *fakeLiveMigrationRuntime) Start(ctx context.Context, inst kindlingruntime.Instance) (string, error) {
	return "", errors.New("not implemented in test runtime")
}

func (r *fakeLiveMigrationRuntime) Suspend(ctx context.Context, id uuid.UUID) error {
	return kindlingruntime.ErrLiveMigrationUnsupported
}

func (r *fakeLiveMigrationRuntime) Resume(ctx context.Context, id uuid.UUID) (string, error) {
	return "", kindlingruntime.ErrLiveMigrationUnsupported
}

func (r *fakeLiveMigrationRuntime) CreateTemplate(ctx context.Context, id uuid.UUID) (string, error) {
	return "", kindlingruntime.ErrLiveMigrationUnsupported
}

func (r *fakeLiveMigrationRuntime) StartClone(ctx context.Context, inst kindlingruntime.Instance, snapshotRef string, cloneSourceVMID uuid.UUID) (string, kindlingruntime.StartMetadata, error) {
	return "", kindlingruntime.StartMetadata{}, kindlingruntime.ErrLiveMigrationUnsupported
}

func (r *fakeLiveMigrationRuntime) MigrationMetadata(ctx context.Context, id uuid.UUID) (kindlingruntime.MigrationMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return kindlingruntime.MigrationMetadata{}, kindlingruntime.ErrInstanceNotRunning
	}
	return kindlingruntime.MigrationMetadata{
		SharedRootfsRef: inst.sharedRootfsRef,
		Version:         r.version,
	}, nil
}

func (r *fakeLiveMigrationRuntime) PrepareMigrationTarget(ctx context.Context, id uuid.UUID) (kindlingruntime.PreparedMigrationTarget, error) {
	if strings.TrimSpace(r.sharedRoot) == "" {
		return kindlingruntime.PreparedMigrationTarget{}, kindlingruntime.ErrLiveMigrationUnsupported
	}
	r.mu.Lock()
	r.prepareCount++
	port := 24000 + r.prepareCount
	r.mu.Unlock()

	receiveAddr := fmt.Sprintf("0.0.0.0:%d", port)
	r.coord.mu.Lock()
	r.coord.prepared[id] = &fakePreparedMigration{
		target:      r,
		receiveAddr: receiveAddr,
	}
	r.coord.mu.Unlock()
	return kindlingruntime.PreparedMigrationTarget{ReceiveAddr: receiveAddr}, nil
}

func (r *fakeLiveMigrationRuntime) SendMigration(ctx context.Context, id uuid.UUID, req kindlingruntime.SendMigrationRequest) error {
	if r.sendErr != nil {
		return r.sendErr
	}
	r.mu.Lock()
	inst, ok := r.instances[id]
	r.mu.Unlock()
	if !ok {
		return kindlingruntime.ErrInstanceNotRunning
	}

	r.coord.mu.Lock()
	defer r.coord.mu.Unlock()
	prepared, ok := r.coord.prepared[id]
	if !ok {
		return errors.New("destination receiver not prepared")
	}
	if want := "tcp:" + prepared.receiveAddr; req.DestinationURL != want {
		return fmt.Errorf("destination url = %q, want %q", req.DestinationURL, want)
	}
	prepared.payload = inst.payload
	return nil
}

func (r *fakeLiveMigrationRuntime) FinalizeMigrationTarget(ctx context.Context, id uuid.UUID) (string, kindlingruntime.StartMetadata, error) {
	if r.finalizeErr != nil {
		return "", kindlingruntime.StartMetadata{}, r.finalizeErr
	}
	r.coord.mu.Lock()
	prepared, ok := r.coord.prepared[id]
	if ok {
		delete(r.coord.prepared, id)
	}
	r.coord.mu.Unlock()
	if !ok || prepared.target != r {
		return "", kindlingruntime.StartMetadata{}, kindlingruntime.ErrInstanceNotRunning
	}

	inst := &fakeLiveMigrationInstance{
		payload:         prepared.payload,
		sharedRootfsRef: sharedRootfsRef(r.sharedRoot, id),
		runtimeURL:      r.runtimeURL,
	}
	r.mu.Lock()
	r.instances[id] = inst
	r.mu.Unlock()
	return inst.runtimeURL, kindlingruntime.StartMetadata{SharedRootfsRef: inst.sharedRootfsRef}, nil
}

func (r *fakeLiveMigrationRuntime) AbortMigrationTarget(ctx context.Context, id uuid.UUID) error {
	r.coord.mu.Lock()
	delete(r.coord.prepared, id)
	r.coord.mu.Unlock()
	return nil
}

func (r *fakeLiveMigrationRuntime) Stop(ctx context.Context, id uuid.UUID) error {
	r.mu.Lock()
	delete(r.instances, id)
	r.mu.Unlock()
	return nil
}

func (r *fakeLiveMigrationRuntime) Healthy(ctx context.Context, id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.instances[id]
	return ok
}

func (r *fakeLiveMigrationRuntime) Logs(ctx context.Context, id uuid.UUID) ([]string, error) {
	return nil, nil
}

func (r *fakeLiveMigrationRuntime) StopAll() {}

func (r *fakeLiveMigrationRuntime) ResourceStats(ctx context.Context, id uuid.UUID) (kindlingruntime.ResourceStats, error) {
	return kindlingruntime.ResourceStats{}, nil
}

func (r *fakeLiveMigrationRuntime) payload(id uuid.UUID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return "", false
	}
	return inst.payload, true
}
