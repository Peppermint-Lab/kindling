// Package deploy implements the deployment reconciler, which drives the full
// lifecycle: pending → create build → wait for build → create N instances → health check
// → mark running → drain old deployments.
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

// Duration constants for the deployment reconciler.
const buildPollRetryInterval = 10 * time.Second        // retry delay while a build is still in progress
const reconcileRetryInterval = 5 * time.Second         // default retry for instance/volume/ready waits
const healthCheckTimeout = 90 * time.Second            // max wait for workload health check after start/resume
const healthCheckClientTimeout = 10 * time.Second      // per-probe HTTP client timeout
const healthCheckRetryDelay = 100 * time.Millisecond   // brief backoff for transient reset/EOF health probe errors
const healthCheckRetryAttempts = 5                     // tolerate a short burst of transient health probe failures
const healthCheckPollInterval = 100 * time.Millisecond // sleep between successive health check probes

// Deployer orchestrates deployments via reconciliation.
type Deployer struct {
	q                    *queries.Queries
	pool                 *pgxpool.Pool
	serverID             uuid.UUID
	secretDecoder        projectSecretDecoder
	deploymentReconciler *reconciler.Scheduler
	serverScheduler      *reconciler.Scheduler
	rt                   runtime.Runtime
	publishProjectEvents func(projectID uuid.UUID)
	publishServerEvents  func()
}

type projectSecretDecoder interface {
	DecryptProjectSecretValue(string) (string, error)
}

// New creates a new deployer. pool is used for transactional instance slot
// allocation; it may be nil (best-effort single-node).
func New(q *queries.Queries, pool *pgxpool.Pool, serverID uuid.UUID, secretDecoder projectSecretDecoder) *Deployer {
	return &Deployer{q: q, pool: pool, serverID: serverID, secretDecoder: secretDecoder}
}

// SetRuntime sets the runtime for starting instances.
func (d *Deployer) SetRuntime(rt runtime.Runtime) {
	d.rt = rt
}

// SetReconciler sets the deployment reconciler for re-scheduling.
func (d *Deployer) SetReconciler(r *reconciler.Scheduler) {
	d.deploymentReconciler = r
}

// SetServerScheduler schedules the server reconciler when instances leave a draining host.
func (d *Deployer) SetServerScheduler(r *reconciler.Scheduler) {
	d.serverScheduler = r
}

// SetDashboardPublishers configures optional dashboard invalidation hooks.
func (d *Deployer) SetDashboardPublishers(projectEvents func(projectID uuid.UUID), serverEvents func()) {
	d.publishProjectEvents = projectEvents
	d.publishServerEvents = serverEvents
}

// effectiveReplicaCount returns how many instances the deployment reconciler
// should converge to. Scale-to-zero uses projects.scaled_to_zero. When
// wake_requested_at is set (cold start, scale-from-zero, or crash recovery),
// the target is the clamped service+project desired count so traffic can burst
// against full capacity rather than a single-instance floor.
func serviceDesiredReplicaCount(proj queries.Project, service *queries.Service) int32 {
	if service != nil && service.DesiredInstanceCount > 0 {
		return service.DesiredInstanceCount
	}
	return proj.DesiredInstanceCount
}

func effectiveReplicaCount(proj queries.Project, service *queries.Service, dep queries.Deployment) int32 {
	serviceDesired := serviceDesiredReplicaCount(proj, service)
	if dep.DeploymentKind == "preview" {
		if dep.WakeRequestedAt.Valid {
			d := serviceDesired
			if d < 1 {
				return 1
			}
			return d
		}
		if dep.PreviewScaledToZero {
			return 0
		}
		d := serviceDesired
		if d < 1 {
			return 1
		}
		return d
	}
	// Wake from cold start, scale-from-zero, or crash recovery: converge toward the
	// effective service+project replica target (not an artificial single-instance floor),
	// while still respecting project min/max bounds.
	if dep.WakeRequestedAt.Valid {
		return clampDesiredReplicaCount(proj, serviceDesired)
	}
	if proj.ScaledToZero {
		return 0
	}
	return clampDesiredReplicaCount(proj, serviceDesired)
}

func nonZeroReplicaFloor(proj queries.Project) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	if proj.MinInstanceCount > 1 {
		return proj.MinInstanceCount
	}
	return 1
}

func clampDesiredReplicaCount(proj queries.Project, desired int32) int32 {
	if proj.MaxInstanceCount <= 0 {
		return 0
	}
	d := desired
	floor := nonZeroReplicaFloor(proj)
	if d < floor {
		d = floor
	}
	if d > proj.MaxInstanceCount {
		d = proj.MaxInstanceCount
	}
	if d < 0 {
		return 0
	}
	return d
}

func buildRuntimeEnv(envVars []queries.EnvironmentVariable, decoder projectSecretDecoder) ([]string, error) {
	env := make([]string, 0, len(envVars))
	for _, ev := range envVars {
		value := ev.Value
		if decoder != nil {
			plain, err := decoder.DecryptProjectSecretValue(ev.Value)
			if err != nil {
				return nil, fmt.Errorf("decrypt project secret %s: %w", ev.Name, err)
			}
			value = plain
		} else if config.IsEncryptedProjectSecretValue(ev.Value) {
			return nil, fmt.Errorf("decrypt project secret %s: no secret decoder configured", ev.Name)
		}
		env = append(env, fmt.Sprintf("%s=%s", ev.Name, value))
	}
	return env, nil
}

// reconcileContext holds resolved state needed throughout a single deployment
// reconciliation pass.
type reconcileContext struct {
	dep              queries.Deployment
	proj             queries.Project
	service          *queries.Service
	desired          int32
	persistentVolume *runtime.PersistentVolumeMount
	imageRef         string
	env              []string
	logger           *slog.Logger
}

// ReconcileDeployment is the reconcile function for deployments.
func (d *Deployer) ReconcileDeployment(ctx context.Context, deploymentID uuid.UUID) error {
	dep, err := d.q.DeploymentFirstByID(ctx, pguuid.ToPgtype(deploymentID))
	if err != nil {
		return fmt.Errorf("fetch deployment: %w", err)
	}
	proj, err := d.q.ProjectFirstByID(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch project: %w", err)
	}
	var service *queries.Service
	if dep.ServiceID.Valid {
		svc, err := d.q.ServiceFirstByID(ctx, dep.ServiceID)
		if err != nil {
			return fmt.Errorf("fetch service: %w", err)
		}
		service = &svc
	}

	desired := effectiveReplicaCount(proj, service, dep)
	projectVolume, persistentVolume, err := d.projectVolumeForDeployment(ctx, dep)
	if err != nil {
		return fmt.Errorf("fetch project volume: %w", err)
	}
	if projectVolume != nil {
		if dep.DeploymentKind == "preview" {
			d.failPreviewVolumeDeployment(ctx, dep)
			return nil
		}
		if desired > 1 {
			desired = 1
		}
	}

	logger := slog.With("deployment_id", deploymentID, "effective_instances", desired, "desired_instance_count", serviceDesiredReplicaCount(proj, service), "scaled_to_zero", proj.ScaledToZero, "wake", dep.WakeRequestedAt.Valid)

	if dep.DeletedAt.Valid || dep.FailedAt.Valid || dep.StoppedAt.Valid {
		d.reconcileTerminalDeployment(ctx, dep, logger)
		return nil
	}

	dep, done, err := d.ensureBuild(ctx, dep, deploymentID, logger)
	if err != nil || done {
		return err
	}

	persistentVolume, done, err = d.resolveProjectVolume(ctx, dep, projectVolume, persistentVolume, deploymentID, logger)
	if err != nil || done {
		return err
	}

	dep, err = d.syncDeploymentImage(ctx, dep)
	if err != nil {
		return err
	}
	if d.rt == nil {
		return fmt.Errorf("no runtime configured")
	}

	rc := &reconcileContext{
		dep:              dep,
		proj:             proj,
		service:          service,
		desired:          desired,
		persistentVolume: persistentVolume,
		logger:           logger,
	}
	if err := d.prepareImageAndEnv(ctx, rc); err != nil {
		return err
	}

	return d.reconcileInstances(ctx, rc, deploymentID)
}

// failPreviewVolumeDeployment marks a preview deployment as failed because
// persistent volumes are not supported for previews.
func (d *Deployer) failPreviewVolumeDeployment(ctx context.Context, dep queries.Deployment) {
	msg := "persistent volumes are not supported for preview deployments"
	_, _ = d.q.ProjectVolumeUpdateStatus(ctx, queries.ProjectVolumeUpdateStatusParams{
		ProjectID: dep.ProjectID,
		Status:    "unavailable",
		LastError: msg,
	})
	if !dep.FailedAt.Valid {
		_ = d.q.DeploymentUpdateFailedAt(ctx, dep.ID)
	}
}

// reconcileTerminalDeployment cleans up a deployment in a terminal state
// (deleted, failed, or stopped).
func (d *Deployer) reconcileTerminalDeployment(ctx context.Context, dep queries.Deployment, logger *slog.Logger) {
	logger.Info("deployment in terminal state")
	d.cleanupDeploymentAllInstances(ctx, dep.ID)
	if dep.VmID.Valid {
		if vm, err := d.q.VMFirstByID(ctx, dep.VmID); err == nil && !vm.DeletedAt.Valid {
			d.q.VMSoftDelete(ctx, dep.VmID)
		}
	}
}

// reconcileInstances handles instance convergence: scaling, per-instance
// reconciliation, draining, and final promotion.
func (d *Deployer) reconcileInstances(ctx context.Context, rc *reconcileContext, deploymentID uuid.UUID) error {
	instList, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, rc.dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}
	d.repairInstancesOnBadServers(ctx, instList, rc.logger)

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, rc.dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}

	// Scale-to-zero: converge to zero instances; still promote the deployment
	// so domains and routing metadata point at this revision.
	if rc.desired == 0 {
		if err := d.scaleDownInstances(ctx, instList, 0, rc.logger); err != nil {
			return fmt.Errorf("scale down instances: %w", err)
		}
		return d.promoteDeployment(ctx, rc, 0)
	}

	instList, err = d.surgeAndReconcile(ctx, rc, instList, deploymentID)
	if err != nil || instList == nil {
		return err
	}

	instList, err = d.retireDrainingInstances(ctx, rc, instList, deploymentID)
	if err != nil || instList == nil {
		return err
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, rc.dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}
	ready := d.countInstancesReady(ctx, instList)
	if ready < int(rc.desired) {
		rc.logger.Info("waiting for instances to become ready", "ready", ready, "desired", rc.desired)
		d.scheduleRetry(deploymentID, reconcileRetryInterval)
		return nil
	}

	if rc.dep.WakeRequestedAt.Valid {
		if err := d.q.DeploymentClearWakeRequested(ctx, rc.dep.ID); err != nil {
			return fmt.Errorf("clear wake: %w", err)
		}
	}
	return d.promoteDeployment(ctx, rc, ready)
}

// surgeAndReconcile scales up/down to the surge target, then reconciles each
// instance. Returns nil instList when the caller should return early (retry scheduled).
func (d *Deployer) surgeAndReconcile(
	ctx context.Context,
	rc *reconcileContext,
	instList []queries.DeploymentInstance,
	deploymentID uuid.UUID,
) ([]queries.DeploymentInstance, error) {
	statusMap, err := d.serverStatusByIDs(ctx, instList)
	if err != nil {
		return nil, fmt.Errorf("load server statuses: %w", err)
	}
	onDraining := d.countInstancesOnDrainingServers(instList, statusMap)
	surgeTarget := int(rc.desired) + onDraining

	instList, err = d.scaleDownExcess(ctx, rc.dep.ID, instList, surgeTarget, statusMap, rc.logger)
	if err != nil {
		return nil, fmt.Errorf("scale down excess: %w", err)
	}
	if err := d.ensureInstanceCountUp(ctx, rc.dep.ID, int32(surgeTarget)); err != nil {
		return nil, fmt.Errorf("ensure instance count: %w", err)
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, rc.dep.ID)
	if err != nil {
		return nil, fmt.Errorf("list deployment instances: %w", err)
	}
	instList, err = d.activateWarmPoolInstances(ctx, instList, int(rc.desired))
	if err != nil {
		return nil, fmt.Errorf("activate warm pool instances: %w", err)
	}
	templateRef, templateSourceVMID := d.templateSourceForDeployment(ctx, instList, rc.dep.ImageID)
	if rc.persistentVolume != nil {
		templateRef = ""
		templateSourceVMID = pgtype.UUID{}
	}

	for _, inst := range instList {
		if err := d.reconcileOneInstance(ctx, rc.dep, inst, rc.imageRef, rc.env, templateRef, templateSourceVMID, rc.persistentVolume, rc.logger); err != nil {
			rc.logger.Info("instance reconcile deferred", "instance_id", pguuid.FromPgtype(inst.ID), "error", err)
			d.scheduleRetry(deploymentID, reconcileRetryInterval)
			return nil, nil
		}
	}
	return instList, nil
}

// retireDrainingInstances removes instances from draining servers once enough
// ready instances exist elsewhere. Returns nil instList when done early.
func (d *Deployer) retireDrainingInstances(
	ctx context.Context,
	rc *reconcileContext,
	instList []queries.DeploymentInstance,
	deploymentID uuid.UUID,
) ([]queries.DeploymentInstance, error) {
	instList, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, rc.dep.ID)
	if err != nil {
		return nil, fmt.Errorf("list deployment instances: %w", err)
	}
	statusMap, err := d.serverStatusByIDs(ctx, instList)
	if err != nil {
		return nil, fmt.Errorf("load server statuses: %w", err)
	}
	onDraining := d.countInstancesOnDrainingServers(instList, statusMap)
	if d.countReadyOffDrainingServers(ctx, instList, statusMap) >= int(rc.desired) && onDraining > 0 {
		instList, err = d.removeInstancesOnDrainingServers(ctx, rc.dep.ID, instList, statusMap, rc.desired, rc.logger)
		if err != nil {
			return nil, fmt.Errorf("remove draining instances: %w", err)
		}
		statusMap, err = d.serverStatusByIDs(ctx, instList)
		if err != nil {
			return nil, fmt.Errorf("load server statuses: %w", err)
		}
		onDraining = d.countInstancesOnDrainingServers(instList, statusMap)
	}
	if onDraining == 0 && d.countActiveInstances(instList) > int(rc.desired) {
		instList, err = d.scaleDownExcess(ctx, rc.dep.ID, instList, int(rc.desired), statusMap, rc.logger)
		if err != nil {
			return nil, fmt.Errorf("scale down excess instances: %w", err)
		}
	}
	return instList, nil
}

// promoteDeployment marks the deployment running and updates domain routing.
func (d *Deployer) promoteDeployment(ctx context.Context, rc *reconcileContext, ready int) error {
	if rc.dep.RunningAt.Valid {
		return nil
	}
	if err := d.q.DeploymentMarkRunning(ctx, rc.dep.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	// Reset circuit breaker on successful promotion (instances are healthy).
	if err := d.q.DeploymentResetCircuit(ctx, rc.dep.ID); err != nil {
		rc.logger.Warn("reset circuit breaker after promotion", "deployment_id", rc.dep.ID, "error", err)
	}
	if ready == 0 {
		rc.logger.Info("deployment is running (scaled to zero instances)")
	} else {
		rc.logger.Info("deployment is running", "instances", ready)
	}
	if rc.dep.DeploymentKind == "preview" {
		if err := d.ensurePreviewRoutes(ctx, rc.dep, rc.proj, rc.logger); err != nil {
			return fmt.Errorf("preview routes: %w", err)
		}
	} else {
		if err := d.ensureProductionRoutes(ctx, rc.dep, rc.proj, rc.service, rc.logger); err != nil {
			return fmt.Errorf("production routes: %w", err)
		}
		d.drainOldDeployments(ctx, rc.dep)
	}
	return nil
}

func (d *Deployer) scheduleRetry(id uuid.UUID, delay time.Duration) {
	if d.deploymentReconciler != nil {
		d.deploymentReconciler.Schedule(id, time.Now().Add(delay))
	}
}
