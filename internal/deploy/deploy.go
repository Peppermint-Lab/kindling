// Package deploy implements the deployment reconciler, which drives the full
// lifecycle: pending → create build → wait for build → create N instances → health check
// → mark running → drain old deployments.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
)

// Deployer orchestrates deployments via reconciliation.
type Deployer struct {
	q                    *queries.Queries
	pool                 *pgxpool.Pool
	serverID             uuid.UUID
	deploymentReconciler *reconciler.Scheduler
	rt                   runtime.Runtime
}

// New creates a new deployer. pool is used for transactional instance slot
// allocation; it may be nil (best-effort single-node).
func New(q *queries.Queries, pool *pgxpool.Pool, serverID uuid.UUID) *Deployer {
	return &Deployer{q: q, pool: pool, serverID: serverID}
}

// SetRuntime sets the runtime for starting instances.
func (d *Deployer) SetRuntime(rt runtime.Runtime) {
	d.rt = rt
}

// SetReconciler sets the deployment reconciler for re-scheduling.
func (d *Deployer) SetReconciler(r *reconciler.Scheduler) {
	d.deploymentReconciler = r
}

// ReconcileDeployment is the reconcile function for deployments.
func (d *Deployer) ReconcileDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	dep, err := d.q.DeploymentFirstByID(ctx, uuidToPgtype(deploymentID))
	if err != nil {
		return fmt.Errorf("fetch deployment: %w", err)
	}

	proj, err := d.q.ProjectFirstByID(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch project: %w", err)
	}
	desired := proj.DesiredInstanceCount
	if desired < 1 {
		desired = 1
	}

	logger := slog.With("deployment_id", deploymentID, "desired_instances", desired)

	if dep.DeletedAt.Valid || dep.FailedAt.Valid || dep.StoppedAt.Valid {
		logger.Info("deployment in terminal state")
		d.cleanupDeploymentAllInstances(ctx, dep.ID)
		if dep.VmID.Valid {
			if vm, err := d.q.VMFirstByID(ctx, dep.VmID); err == nil && !vm.DeletedAt.Valid {
				d.q.VMSoftDelete(ctx, dep.VmID)
			}
		}
		return nil
	}

	if !dep.BuildID.Valid {
		logger.Info("creating build for deployment")
		build, err := d.q.BuildCreate(ctx, queries.BuildCreateParams{
			ID:           uuidToPgtype(uuid.New()),
			ProjectID:    dep.ProjectID,
			Status:       "pending",
			GithubCommit: dep.GithubCommit,
			GithubBranch: "main",
		})
		if err != nil {
			return fmt.Errorf("create build: %w", err)
		}
		dep, err = d.q.DeploymentUpdateBuild(ctx, queries.DeploymentUpdateBuildParams{
			ID:      dep.ID,
			BuildID: build.ID,
		})
		if err != nil {
			return fmt.Errorf("update deployment build: %w", err)
		}
		logger.Info("build created", "build_id", build.ID)
		return nil
	}

	build, err := d.q.BuildFirstByID(ctx, dep.BuildID)
	if err != nil {
		return fmt.Errorf("fetch build: %w", err)
	}
	if build.FailedAt.Valid {
		logger.Info("build failed, failing deployment")
		return d.q.DeploymentUpdateFailedAt(ctx, dep.ID)
	}
	if !build.ImageID.Valid {
		logger.Info("build in progress, will retry")
		d.scheduleRetry(deploymentID, 10*time.Second)
		return nil
	}

	if dep.ImageID != build.ImageID {
		dep, err = d.q.DeploymentUpdateImage(ctx, queries.DeploymentUpdateImageParams{
			ID:      dep.ID,
			ImageID: build.ImageID,
		})
		if err != nil {
			return fmt.Errorf("update deployment image: %w", err)
		}
	}

	if d.rt == nil {
		return fmt.Errorf("no runtime configured")
	}

	image, err := d.q.ImageFindByID(ctx, dep.ImageID)
	if err != nil {
		return fmt.Errorf("fetch image: %w", err)
	}
	imageRef := fmt.Sprintf("%s/%s:%s", image.Registry, image.Repository, image.Tag)

	envVars, err := d.q.EnvironmentVariableFindByProjectID(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch env vars: %w", err)
	}
	var env []string
	for _, ev := range envVars {
		env = append(env, fmt.Sprintf("%s=%s", ev.Name, ev.Value))
	}

	instList, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}

	d.repairInstancesOnBadServers(ctx, instList, logger)

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}

	if err := d.scaleDownInstances(ctx, instList, int(desired), logger); err != nil {
		return err
	}

	if err := d.ensureInstanceCountUp(ctx, dep.ID, desired); err != nil {
		return err
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}

	for _, inst := range instList {
		if err := d.reconcileOneInstance(ctx, dep, inst, imageRef, env, logger); err != nil {
			logger.Info("instance reconcile deferred", "instance_id", uuidFromPgtype(inst.ID), "error", err)
			d.scheduleRetry(deploymentID, 5*time.Second)
			return nil
		}
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}
	ready := d.countInstancesReady(ctx, instList)
	if ready < int(desired) {
		logger.Info("waiting for instances to become ready", "ready", ready, "desired", desired)
		d.scheduleRetry(deploymentID, 5*time.Second)
		return nil
	}

	if !dep.RunningAt.Valid {
		if err := d.q.DeploymentMarkRunning(ctx, dep.ID); err != nil {
			return fmt.Errorf("mark running: %w", err)
		}
		logger.Info("deployment is running", "instances", ready)
		d.q.DomainUpdateDeploymentForProject(ctx, queries.DomainUpdateDeploymentForProjectParams{
			DeploymentID: dep.ID,
			ProjectID:    dep.ProjectID,
		})
		d.drainOldDeployments(ctx, dep)
	}

	return nil
}

func (d *Deployer) cleanupDeploymentAllInstances(ctx context.Context, deploymentID pgtype.UUID) {
	instList, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
	if err != nil {
		return
	}
	for _, inst := range instList {
		d.cleanupInstance(ctx, inst)
	}
}

func (d *Deployer) cleanupInstance(ctx context.Context, inst queries.DeploymentInstance) {
	iid := uuidFromPgtype(inst.ID)
	if d.rt != nil && iid != uuid.Nil {
		_ = d.rt.Stop(ctx, iid)
	}
	if inst.VmID.Valid {
		_ = d.q.VMSoftDelete(ctx, inst.VmID)
	}
	_ = d.q.DeploymentInstanceSoftDelete(ctx, inst.ID)
}

func (d *Deployer) repairInstancesOnBadServers(ctx context.Context, instList []queries.DeploymentInstance, logger *slog.Logger) {
	for _, inst := range instList {
		if !inst.ServerID.Valid {
			continue
		}
		srv, err := d.q.ServerFindByID(ctx, inst.ServerID)
		if err != nil {
			continue
		}
		if srv.Status != "dead" && srv.Status != "drained" {
			continue
		}
		logger.Info("resetting instance on unavailable server", "instance_id", uuidFromPgtype(inst.ID), "server_status", srv.Status)
		if d.rt != nil {
			_ = d.rt.Stop(ctx, uuidFromPgtype(inst.ID))
		}
		if inst.VmID.Valid {
			_ = d.q.VMSoftDelete(ctx, inst.VmID)
		}
		if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
			logger.Warn("prepare instance retry failed", "error", err)
		}
	}
}

func (d *Deployer) scaleDownInstances(ctx context.Context, instList []queries.DeploymentInstance, desired int, logger *slog.Logger) error {
	if len(instList) <= desired {
		return nil
	}
	// Remove newest instances first.
	sorted := slices.Clone(instList)
	slices.SortFunc(sorted, func(a, b queries.DeploymentInstance) int {
		if a.CreatedAt.Time.Equal(b.CreatedAt.Time) {
			return 0
		}
		if a.CreatedAt.Time.After(b.CreatedAt.Time) {
			return -1
		}
		return 1
	})
	excess := len(sorted) - desired
	for i := 0; i < excess; i++ {
		logger.Info("scaling down instance", "instance_id", uuidFromPgtype(sorted[i].ID))
		d.cleanupInstance(ctx, sorted[i])
	}
	return nil
}

func (d *Deployer) ensureInstanceCountUp(ctx context.Context, deploymentID pgtype.UUID, desired int32) error {
	if d.pool != nil {
		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback(ctx)
		qtx := queries.New(tx)
		if err := qtx.AdvisoryLock(ctx, "kindling:deploy:"+uuidFromPgtype(deploymentID).String()); err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}
		rows, err := qtx.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
		if err != nil {
			return err
		}
		for len(rows) < int(desired) {
			if _, err := qtx.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
				ID:           uuidToPgtype(uuid.New()),
				DeploymentID: deploymentID,
			}); err != nil {
				return fmt.Errorf("create deployment instance: %w", err)
			}
			rows, err = qtx.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
			if err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	rows, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
	if err != nil {
		return err
	}
	for len(rows) < int(desired) {
		if _, err := d.q.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
			ID:           uuidToPgtype(uuid.New()),
			DeploymentID: deploymentID,
		}); err != nil {
			return fmt.Errorf("create deployment instance: %w", err)
		}
		rows, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Deployer) reconcileOneInstance(
	ctx context.Context,
	dep queries.Deployment,
	inst queries.DeploymentInstance,
	imageRef string,
	env []string,
	logger *slog.Logger,
) error {
	if inst.Status == "running" && inst.VmID.Valid {
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err == nil && !vm.DeletedAt.Valid && vm.Status == "running" {
			port := 3000
			if vm.Port.Valid {
				port = int(vm.Port.Int32)
			}
			if requiresExternalHealthCheck(d.rt.Name()) && !d.healthCheckVMFromHost(vm, port) {
				return fmt.Errorf("instance failed health check")
			}
			return nil
		}
	}

	if inst.VmID.Valid || inst.Status == "failed" {
		if d.rt != nil {
			_ = d.rt.Stop(ctx, uuidFromPgtype(inst.ID))
		}
		if inst.VmID.Valid {
			_ = d.q.VMSoftDelete(ctx, inst.VmID)
		}
		if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
			return fmt.Errorf("prepare retry: %w", err)
		}
		inst, _ = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
	}

	if !inst.ServerID.Valid {
		srv, err := d.q.ServerFindLeastLoaded(ctx)
		if err != nil {
			return fmt.Errorf("pick server: %w", err)
		}
		updated, err := d.q.DeploymentInstanceUpdateServer(ctx, queries.DeploymentInstanceUpdateServerParams{
			ID:       inst.ID,
			ServerID: srv.ID,
		})
		if err != nil {
			return fmt.Errorf("assign server: %w", err)
		}
		inst = updated
	}

	if uuidFromPgtype(inst.ServerID) != d.serverID {
		return nil
	}

	if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
		ID:     inst.ID,
		Status: "starting",
	}); err != nil {
		return fmt.Errorf("mark starting: %w", err)
	}

	instID := uuidFromPgtype(inst.ID)
	logger.Info("starting instance", "instance_id", instID, "image", imageRef, "runtime", d.rt.Name())
	ip, err := d.rt.Start(ctx, runtime.Instance{
		ID:       instID,
		ImageRef: imageRef,
		VCPUs:    1,
		MemoryMB: 512,
		Port:     3000,
		Env:      env,
	})
	if err != nil {
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("start instance: %w", err)
	}

	if requiresExternalHealthCheck(d.rt.Name()) && !d.healthCheckLocalForwarded(ip) {
		_ = d.rt.Stop(ctx, instID)
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("health check failed")
	}

	if err := d.persistInstanceVMMetadata(ctx, d.q, inst.ID, dep.ImageID, uuidFromPgtype(inst.ServerID), ip, 1, 512, env); err != nil {
		_ = d.rt.Stop(ctx, instID)
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("persist vm metadata: %w", err)
	}
	return nil
}

func (d *Deployer) countInstancesReady(ctx context.Context, instList []queries.DeploymentInstance) int {
	n := 0
	for _, inst := range instList {
		if inst.Status != "running" || !inst.VmID.Valid {
			continue
		}
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err != nil || vm.DeletedAt.Valid || vm.Status != "running" {
			continue
		}
		port := 3000
		if vm.Port.Valid {
			port = int(vm.Port.Int32)
		}
		if requiresExternalHealthCheck(d.rt.Name()) && !d.healthCheckVMFromHost(vm, port) {
			continue
		}
		n++
	}
	return n
}

func (d *Deployer) healthCheck(addr string, port int) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	url := addr
	if !strings.Contains(addr, ":") {
		url = fmt.Sprintf("%s:%d", addr, port)
	}
	if !strings.HasPrefix(url, "http") {
		url = "http://" + url
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// healthCheckVMFromHost probes a VM row. Workloads on this kindling server
// bind a TCP port on the host; the DB stores the public/advertised IP, which
// many hosts cannot reach via hairpin NAT. Use loopback when the VM belongs
// to this server.
func (d *Deployer) healthCheckVMFromHost(vm queries.Vm, port int) bool {
	if vm.ServerID.Valid && uuidFromPgtype(vm.ServerID) == d.serverID {
		return d.healthCheck("127.0.0.1", port)
	}
	return d.healthCheck(vm.IpAddress.String(), port)
}

// healthCheckLocalForwarded checks a runtime URL returned by the local runtime
// (public IP + host port). The forwarder always listens on all interfaces, so
// loopback reaches the same port.
func (d *Deployer) healthCheckLocalForwarded(runtimeURL string) bool {
	_, port, err := parseRuntimeAddress(runtimeURL)
	if err != nil {
		return false
	}
	return d.healthCheck("127.0.0.1", port)
}

func requiresExternalHealthCheck(runtimeName string) bool {
	return runtimeName != "apple-vz"
}

func (d *Deployer) drainOldDeployments(ctx context.Context, current queries.Deployment) {
	old, err := d.q.DeploymentFindRunningAndOlder(ctx, queries.DeploymentFindRunningAndOlderParams{
		ProjectID: current.ProjectID,
		ID:        current.ID,
	})
	if err != nil || len(old) == 0 {
		return
	}

	slog.Info("draining old deployments", "count", len(old))
	for _, o := range old {
		d.cleanupDeploymentAllInstances(ctx, o.ID)
		if o.VmID.Valid {
			_ = d.q.VMSoftDelete(ctx, o.VmID)
		}
		_ = d.q.DeploymentMarkStopped(ctx, o.ID)
	}
}

func (d *Deployer) scheduleRetry(id uuid.UUID, delay time.Duration) {
	if d.deploymentReconciler != nil {
		d.deploymentReconciler.Schedule(id, time.Now().Add(delay))
	}
}

func uuidToPgtype(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func uuidFromPgtype(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return u.Bytes
}
