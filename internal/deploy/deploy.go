// Package deploy implements the deployment reconciler, which drives the full
// lifecycle: pending → create build → wait for build → create N instances → health check
// → mark running → drain old deployments.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/preview"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

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
// should converge to. Scale-to-zero uses projects.scaled_to_zero; cold start
// uses deployments.wake_requested_at to temporarily raise the count.
func effectiveReplicaCount(proj queries.Project, dep queries.Deployment) int32 {
	if dep.DeploymentKind == "preview" {
		if dep.WakeRequestedAt.Valid {
			d := proj.DesiredInstanceCount
			if d < 1 {
				return 1
			}
			return d
		}
		if dep.PreviewScaledToZero {
			return 0
		}
		d := proj.DesiredInstanceCount
		if d < 1 {
			return 1
		}
		return d
	}
	if dep.WakeRequestedAt.Valid {
		d := proj.DesiredInstanceCount
		if d < 1 {
			return 1
		}
		return d
	}
	if proj.ScaledToZero {
		return 0
	}
	d := proj.DesiredInstanceCount
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
	desired := effectiveReplicaCount(proj, dep)
	projectVolume, persistentVolume, err := d.projectVolumeForProject(ctx, dep.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch project volume: %w", err)
	}
	if projectVolume != nil {
		if dep.DeploymentKind == "preview" {
			msg := "persistent volumes are not supported for preview deployments"
			_, _ = d.q.ProjectVolumeUpdateStatus(ctx, queries.ProjectVolumeUpdateStatusParams{
				ProjectID: dep.ProjectID,
				Status:    "unavailable",
				LastError: msg,
			})
			if !dep.FailedAt.Valid {
				_ = d.q.DeploymentUpdateFailedAt(ctx, dep.ID)
			}
			return nil
		}
		if desired > 1 {
			desired = 1
		}
	}

	logger := slog.With("deployment_id", deploymentID, "effective_instances", desired, "desired_instance_count", proj.DesiredInstanceCount, "scaled_to_zero", proj.ScaledToZero, "wake", dep.WakeRequestedAt.Valid)

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
		branch := strings.TrimSpace(dep.GithubBranch)
		if branch == "" {
			branch = "main"
		}
		build, err := d.q.BuildCreate(ctx, queries.BuildCreateParams{
			ID:           pguuid.ToPgtype(uuid.New()),
			ProjectID:    dep.ProjectID,
			Status:       "pending",
			GithubCommit: dep.GithubCommit,
			GithubBranch: branch,
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

	if projectVolume != nil {
		resolvedVolume, err := d.ensureProjectVolumeServer(ctx, *projectVolume)
		if err != nil {
			logger.Warn("project volume unavailable", "project_id", pguuid.FromPgtype(dep.ProjectID), "error", err)
			d.scheduleRetry(deploymentID, projectVolumeRetryDelay(err))
			return nil
		}
		projectVolume = &resolvedVolume
		persistentVolume = persistentVolumeMountFromRow(resolvedVolume)
		if dep.DeploymentKind == "production" && !dep.RunningAt.Valid {
			drained, err := d.stopOldDeploymentsForVolume(ctx, dep, logger)
			if err != nil {
				return fmt.Errorf("stop old deployments for volume: %w", err)
			}
			if drained {
				d.scheduleRetry(deploymentID, 5*time.Second)
				return nil
			}
		}
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
	env, err := buildRuntimeEnv(envVars, d.secretDecoder)
	if err != nil {
		return err
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

	// Scale-to-zero: converge to zero instances; still promote the deployment
	// so domains and routing metadata point at this revision.
	if desired == 0 {
		if err := d.scaleDownInstances(ctx, instList, 0, logger); err != nil {
			return err
		}
		if !dep.RunningAt.Valid {
			if err := d.q.DeploymentMarkRunning(ctx, dep.ID); err != nil {
				return fmt.Errorf("mark running: %w", err)
			}
			logger.Info("deployment is running (scaled to zero instances)")
			if dep.DeploymentKind == "preview" {
				if err := d.ensurePreviewRoutes(ctx, dep, proj, logger); err != nil {
					return fmt.Errorf("preview routes: %w", err)
				}
			} else {
				if err := d.q.DomainUpdateDeploymentForProject(ctx, queries.DomainUpdateDeploymentForProjectParams{
					DeploymentID: dep.ID,
					ProjectID:    dep.ProjectID,
				}); err != nil {
					return fmt.Errorf("update domain deployment: %w", err)
				}
				d.drainOldDeployments(ctx, dep)
			}
		}
		return nil
	}

	statusMap, err := d.serverStatusByIDs(ctx, instList)
	if err != nil {
		return fmt.Errorf("load server statuses: %w", err)
	}
	onDraining := d.countInstancesOnDrainingServers(instList, statusMap)
	surgeTarget := int(desired) + onDraining

	instList, err = d.scaleDownExcess(ctx, dep.ID, instList, surgeTarget, statusMap, logger)
	if err != nil {
		return err
	}

	if err := d.ensureInstanceCountUp(ctx, dep.ID, int32(surgeTarget)); err != nil {
		return err
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}
	templateRef, templateSourceVMID := d.templateSourceForDeployment(ctx, instList, dep.ImageID)
	if persistentVolume != nil {
		templateRef = ""
		templateSourceVMID = pgtype.UUID{}
	}

	for _, inst := range instList {
		if err := d.reconcileOneInstance(ctx, dep, inst, imageRef, env, templateRef, templateSourceVMID, persistentVolume, logger); err != nil {
			logger.Info("instance reconcile deferred", "instance_id", pguuid.FromPgtype(inst.ID), "error", err)
			d.scheduleRetry(deploymentID, 5*time.Second)
			return nil
		}
	}

	instList, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
	if err != nil {
		return fmt.Errorf("list deployment instances: %w", err)
	}
	statusMap, err = d.serverStatusByIDs(ctx, instList)
	if err != nil {
		return fmt.Errorf("load server statuses: %w", err)
	}
	onDraining = d.countInstancesOnDrainingServers(instList, statusMap)
	if d.countReadyOffDrainingServers(ctx, instList, statusMap) >= int(desired) && onDraining > 0 {
		instList, err = d.removeInstancesOnDrainingServers(ctx, dep.ID, instList, statusMap, desired, logger)
		if err != nil {
			return err
		}
		statusMap, err = d.serverStatusByIDs(ctx, instList)
		if err != nil {
			return fmt.Errorf("load server statuses: %w", err)
		}
		onDraining = d.countInstancesOnDrainingServers(instList, statusMap)
	}
	if onDraining == 0 && d.countActiveInstances(instList) > int(desired) {
		instList, err = d.scaleDownExcess(ctx, dep.ID, instList, int(desired), statusMap, logger)
		if err != nil {
			return err
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

	if dep.WakeRequestedAt.Valid {
		if err := d.q.DeploymentClearWakeRequested(ctx, dep.ID); err != nil {
			return fmt.Errorf("clear wake: %w", err)
		}
	}

	if !dep.RunningAt.Valid {
		if err := d.q.DeploymentMarkRunning(ctx, dep.ID); err != nil {
			return fmt.Errorf("mark running: %w", err)
		}
		logger.Info("deployment is running", "instances", ready)
		if dep.DeploymentKind == "preview" {
			if err := d.ensurePreviewRoutes(ctx, dep, proj, logger); err != nil {
				return fmt.Errorf("preview routes: %w", err)
			}
		} else {
			if err := d.q.DomainUpdateDeploymentForProject(ctx, queries.DomainUpdateDeploymentForProjectParams{
				DeploymentID: dep.ID,
				ProjectID:    dep.ProjectID,
			}); err != nil {
				return fmt.Errorf("update domain deployment: %w", err)
			}
			d.drainOldDeployments(ctx, dep)
		}
	}

	return nil
}

func (d *Deployer) ensurePreviewRoutes(ctx context.Context, dep queries.Deployment, proj queries.Project, logger *slog.Logger) error {
	if dep.DeploymentKind != "preview" || !dep.PreviewEnvironmentID.Valid {
		return nil
	}
	base, err := d.q.ClusterSettingGet(ctx, config.SettingPreviewBaseDomain)
	if err != nil || strings.TrimSpace(base) == "" {
		return nil
	}
	base = strings.TrimSpace(base)

	pe, err := d.q.PreviewEnvironmentByID(ctx, dep.PreviewEnvironmentID)
	if err != nil {
		return fmt.Errorf("preview env: %w", err)
	}

	stableDom, err := d.q.DomainFindByPreviewEnvironmentAndKind(ctx, queries.DomainFindByPreviewEnvironmentAndKindParams{
		PreviewEnvironmentID: pe.ID,
		DomainKind:           "preview_stable",
	})
	if err == nil {
		if err := d.q.DomainUpdateDeploymentForDomainID(ctx, queries.DomainUpdateDeploymentForDomainIDParams{
			ID:           stableDom.ID,
			DeploymentID: dep.ID,
		}); err != nil {
			return fmt.Errorf("update stable preview domain: %w", err)
		}
	}

	sha := strings.TrimSpace(dep.GithubCommit)
	if len(sha) > 7 {
		sha = sha[:7]
	}
	immutableHost := preview.ImmutableHostname(sha, int(pe.PrNumber), proj.Name, base)

	_, err = d.q.DomainFindByDeploymentIDAndKind(ctx, queries.DomainFindByDeploymentIDAndKindParams{
		DeploymentID: dep.ID,
		DomainKind:   "preview_immutable",
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup immutable preview domain: %w", err)
		}
		if _, err := d.q.DomainCreatePreview(ctx, queries.DomainCreatePreviewParams{
			ID:                   pguuid.ToPgtype(uuid.New()),
			ProjectID:            dep.ProjectID,
			DeploymentID:         dep.ID,
			DomainName:           immutableHost,
			DomainKind:           "preview_immutable",
			PreviewEnvironmentID: pe.ID,
		}); err != nil {
			return fmt.Errorf("create immutable preview domain: %w", err)
		}
		logger.Info("preview immutable domain", "host", immutableHost)
	}
	return nil
}

func (d *Deployer) cleanupDeploymentAllInstances(ctx context.Context, deploymentID pgtype.UUID) {
	instList, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
	if err != nil {
		return
	}
	for _, inst := range instList {
		d.deleteInstancePermanently(ctx, inst)
	}
}

func (d *Deployer) cleanupInstance(ctx context.Context, inst queries.DeploymentInstance) {
	iid := pguuid.FromPgtype(inst.ID)
	if inst.VmID.Valid && d.rt != nil && d.rt.Supports(runtime.CapabilitySuspendResume) && inst.ServerID.Valid && pguuid.FromPgtype(inst.ServerID) == d.serverID {
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err == nil && !vm.DeletedAt.Valid && vm.Status == "running" {
			if _, err := d.q.DeploymentInstanceUpdateRole(ctx, queries.DeploymentInstanceUpdateRoleParams{
				ID:   inst.ID,
				Role: deploymentInstanceRoleWarmPool,
			}); err != nil {
				slog.Warn("mark warm pool instance failed", "instance_id", iid, "error", err)
				d.deleteInstancePermanently(ctx, inst)
				return
			}
			if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "stopped",
			}); err != nil {
				slog.Warn("mark instance stopped failed", "instance_id", iid, "error", err)
				d.deleteInstancePermanently(ctx, inst)
				return
			}
			if _, err := d.q.VMUpdateLifecycleMetadata(ctx, queries.VMUpdateLifecycleMetadataParams{
				ID:              inst.VmID,
				Status:          vmStatusSuspending,
				SnapshotRef:     vm.SnapshotRef,
				SharedRootfsRef: vm.SharedRootfsRef,
				CloneSourceVmID: vm.CloneSourceVmID,
			}); err != nil {
				slog.Warn("mark vm suspending failed", "instance_id", iid, "error", err)
				d.deleteInstancePermanently(ctx, inst)
				return
			}
			if err := d.rt.Suspend(ctx, iid); err == nil {
				snapshotRef := vm.SnapshotRef
				if d.rt.Supports(runtime.CapabilityWarmClone) {
					if newSnapshotRef, templateErr := d.rt.CreateTemplate(ctx, iid); templateErr == nil && strings.TrimSpace(newSnapshotRef) != "" {
						snapshotRef = pgtype.Text{String: newSnapshotRef, Valid: true}
					}
				}
				if _, err := d.q.VMUpdateLifecycleMetadata(ctx, queries.VMUpdateLifecycleMetadataParams{
					ID:              inst.VmID,
					Status:          vmStatusSuspended,
					SnapshotRef:     snapshotRef,
					SharedRootfsRef: vm.SharedRootfsRef,
					CloneSourceVmID: vm.CloneSourceVmID,
				}); err == nil {
					return
				}
				slog.Warn("mark vm suspended failed", "instance_id", iid)
			}
		}
	}
	if d.rt != nil && iid != uuid.Nil {
		_ = d.rt.Stop(ctx, iid)
	}
	if inst.VmID.Valid {
		_ = d.detachProjectVolumeForInstance(ctx, inst, "available", "")
		_ = d.q.VMSoftDelete(ctx, inst.VmID)
	}
	_ = d.q.DeploymentInstanceSoftDelete(ctx, inst.ID)
}

func (d *Deployer) deleteInstancePermanently(ctx context.Context, inst queries.DeploymentInstance) {
	iid := pguuid.FromPgtype(inst.ID)
	if d.rt != nil && iid != uuid.Nil {
		_ = d.rt.Stop(ctx, iid)
	}
	if inst.VmID.Valid {
		_ = d.detachProjectVolumeForInstance(ctx, inst, "available", "")
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
		logger.Info("resetting instance on unavailable server", "instance_id", pguuid.FromPgtype(inst.ID), "server_status", srv.Status)
		if d.rt != nil {
			_ = d.rt.Stop(ctx, pguuid.FromPgtype(inst.ID))
		}
		if inst.VmID.Valid {
			handledUnavailable, volumeErr := d.markProjectVolumeUnavailableForInstance(ctx, inst, srv.Status)
			if volumeErr != nil {
				logger.Warn("mark project volume unavailable failed", "instance_id", pguuid.FromPgtype(inst.ID), "error", volumeErr)
			} else if !handledUnavailable {
				_ = d.detachProjectVolumeForInstance(ctx, inst, "available", "")
			}
			_ = d.q.VMSoftDelete(ctx, inst.VmID)
		}
		if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
			logger.Warn("prepare instance retry failed", "error", err)
		}
	}
}

func (d *Deployer) scaleDownInstances(ctx context.Context, instList []queries.DeploymentInstance, desired int, logger *slog.Logger) error {
	if d.countActiveInstances(instList) <= desired {
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
	excess := d.countActiveInstances(sorted) - desired
	for _, inst := range sorted {
		if excess <= 0 {
			break
		}
		if !isActiveInstance(inst) {
			continue
		}
		logger.Info("scaling down instance", "instance_id", pguuid.FromPgtype(inst.ID))
		d.cleanupInstance(ctx, inst)
		excess--
	}
	if len(instList) == 0 {
		return nil
	}
	refreshed, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, instList[0].DeploymentID)
	if err != nil {
		return err
	}
	return d.pruneWarmPoolInstances(ctx, refreshed, d.retainedWarmPoolBudget())
}

func (d *Deployer) ensureInstanceCountUp(ctx context.Context, deploymentID pgtype.UUID, desired int32) error {
	if d.pool != nil {
		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback(ctx)
		qtx := queries.New(tx)
		if err := qtx.AdvisoryLock(ctx, "kindling:deploy:"+pguuid.FromPgtype(deploymentID).String()); err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}
		rows, err := qtx.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
		if err != nil {
			return err
		}
		for d.countProvisionableInstances(rows) < int(desired) {
			if _, err := qtx.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
				ID:           pguuid.ToPgtype(uuid.New()),
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
	for d.countProvisionableInstances(rows) < int(desired) {
		if _, err := d.q.DeploymentInstanceCreate(ctx, queries.DeploymentInstanceCreateParams{
			ID:           pguuid.ToPgtype(uuid.New()),
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
	templateRef string,
	templateSourceVMID pgtype.UUID,
	persistentVolume *runtime.PersistentVolumeMount,
	logger *slog.Logger,
) error {
	if inst.Role == deploymentInstanceRoleTemplate {
		return nil
	}
	if inst.ServerID.Valid {
		if persistentVolume != nil {
			projectVolume, _, err := d.projectVolumeForProject(ctx, dep.ProjectID)
			if err != nil {
				return fmt.Errorf("load project volume: %w", err)
			}
			if projectVolume != nil && projectVolume.ServerID.Valid && inst.ServerID != projectVolume.ServerID {
				if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
					return fmt.Errorf("reset instance for pinned volume server: %w", err)
				}
				inst, err = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
				if err != nil {
					return fmt.Errorf("re-fetch instance after volume server reset: %w", err)
				}
				if !inst.ServerID.Valid {
					return nil
				}
			}
		}
		srv, err := d.q.ServerFindByID(ctx, inst.ServerID)
		if err == nil && srv.Status == "draining" {
			if isWarmPoolInstance(inst) {
				d.deleteInstancePermanently(ctx, inst)
				return nil
			}
			if inst.VmID.Valid {
				if vm, vmErr := d.q.VMFirstByID(ctx, inst.VmID); vmErr == nil && vm.Status == vmStatusSuspended {
					d.deleteInstancePermanently(ctx, inst)
					return nil
				}
			}
			if inst.Status != "running" || !inst.VmID.Valid {
				if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
					return fmt.Errorf("release instance from draining server: %w", err)
				}
				inst, err = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
				if err != nil {
					return fmt.Errorf("re-fetch instance after draining server release: %w", err)
				}
			}
		}
	}

	if inst.VmID.Valid {
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err == nil && vm.Status == vmStatusSuspended && (isActiveInstance(inst) || isWarmPoolInstance(inst)) {
			if pguuid.FromPgtype(inst.ServerID) != d.serverID {
				return nil
			}
			if isWarmPoolInstance(inst) {
				updated, updateErr := d.q.DeploymentInstanceUpdateRole(ctx, queries.DeploymentInstanceUpdateRoleParams{
					ID:   inst.ID,
					Role: deploymentInstanceRoleActive,
				})
				if updateErr != nil {
					return fmt.Errorf("promote warm pool instance: %w", updateErr)
				}
				inst = updated
			}
			if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "starting",
			}); err != nil {
				return fmt.Errorf("mark resuming: %w", err)
			}
			ip, err := d.rt.Resume(ctx, pguuid.FromPgtype(inst.ID))
			if err != nil {
				if errors.Is(err, runtime.ErrInstanceNotRunning) {
					if inst.VmID.Valid {
						if persistentVolume != nil {
							_ = d.detachProjectVolumeIfAttached(ctx, dep.ProjectID, inst.VmID, "available", "")
						}
						_ = d.q.VMSoftDelete(ctx, inst.VmID)
					}
					if _, prepErr := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); prepErr != nil {
						return fmt.Errorf("reset stale suspended instance: %w", prepErr)
					}
					return nil
				}
				return fmt.Errorf("resume instance: %w", err)
			}
			if requiresExternalHealthCheck(d.rt.Name()) && !d.waitHealthCheckLocalForwarded(ip, 90*time.Second) {
				return fmt.Errorf("health check failed after resume")
			}
			if _, err := d.q.VMUpdateLifecycleMetadata(ctx, queries.VMUpdateLifecycleMetadataParams{
				ID:              inst.VmID,
				Status:          "running",
				SnapshotRef:     vm.SnapshotRef,
				SharedRootfsRef: vm.SharedRootfsRef,
				CloneSourceVmID: vm.CloneSourceVmID,
			}); err != nil {
				return fmt.Errorf("mark resumed vm running: %w", err)
			}
			if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "running",
			}); err != nil {
				return fmt.Errorf("mark resumed instance running: %w", err)
			}
			return nil
		}
	}

	if inst.Status == "running" && inst.VmID.Valid {
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err == nil {
			port := 3000
			if vm.Port.Valid {
				port = int(vm.Port.Int32)
			}
			hostHealthCheckOK := true
			if d.rt != nil && requiresExternalHealthCheck(d.rt.Name()) {
				hostHealthCheckOK = d.healthCheckVMFromHost(vm, port)
			}
			if shouldKeepRunningVM(vm, d.rt.Name(), hostHealthCheckOK) {
				return nil
			}
		}
	}

	if (inst.VmID.Valid && !isWarmPoolInstance(inst)) || inst.Status == "failed" {
		if d.rt != nil {
			_ = d.rt.Stop(ctx, pguuid.FromPgtype(inst.ID))
		}
		if inst.VmID.Valid {
			if persistentVolume != nil {
				_ = d.detachProjectVolumeIfAttached(ctx, dep.ProjectID, inst.VmID, "available", "")
			}
			_ = d.q.VMSoftDelete(ctx, inst.VmID)
		}
		if _, err := d.q.DeploymentInstancePrepareRetry(ctx, inst.ID); err != nil {
			return fmt.Errorf("prepare retry: %w", err)
		}
		var refetchErr error
		inst, refetchErr = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
		if refetchErr != nil {
			return fmt.Errorf("re-fetch instance after prepare retry: %w", refetchErr)
		}
	}

	if !inst.ServerID.Valid {
		var (
			srv queries.Server
			err error
		)
		if persistentVolume != nil {
			projectVolume, _, volumeErr := d.projectVolumeForProject(ctx, dep.ProjectID)
			if volumeErr != nil {
				return fmt.Errorf("load project volume: %w", volumeErr)
			}
			if projectVolume == nil || !projectVolume.ServerID.Valid {
				return fmt.Errorf("project volume server is not assigned")
			}
			srv, err = d.q.ServerFindByID(ctx, projectVolume.ServerID)
		} else {
			srv, err = d.q.ServerFindLeastLoaded(ctx)
		}
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

	if pguuid.FromPgtype(inst.ServerID) != d.serverID {
		return nil
	}

	if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
		ID:     inst.ID,
		Status: "starting",
	}); err != nil {
		return fmt.Errorf("mark starting: %w", err)
	}

	instID := pguuid.FromPgtype(inst.ID)
	startInst := runtime.Instance{
		ID:               instID,
		ImageRef:         imageRef,
		VCPUs:            1,
		MemoryMB:         512,
		Port:             3000,
		Env:              env,
		PersistentVolume: persistentVolume,
	}
	mode, shouldLaunch := selectLaunchMode(inst, queries.Vm{}, templateRef)
	if !shouldLaunch {
		return nil
	}
	logger.Info("starting instance", "instance_id", instID, "image", imageRef, "runtime", d.rt.Name(), "mode", mode)
	var (
		ip   string
		meta instanceVMMetadata
	)
	switch {
	case mode == launchModeClone && d.rt.Supports(runtime.CapabilityWarmClone):
		startedIP, startMeta, err := d.rt.StartClone(ctx, startInst, templateRef, pguuid.FromPgtype(templateSourceVMID))
		if err != nil {
			if errors.Is(err, runtime.ErrInstanceNotRunning) {
				startedIP, coldErr := d.rt.Start(ctx, startInst)
				if coldErr != nil {
					_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
						ID:     inst.ID,
						Status: "failed",
					})
					return fmt.Errorf("start clone fallback cold start: %w", coldErr)
				}
				ip = startedIP
				meta = instanceVMMetadata{Runtime: d.rt.Name()}
				d.fillLiveMigrationMetadata(ctx, instID, &meta)
				break
			}
			_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "failed",
			})
			return fmt.Errorf("start clone: %w", err)
		}
		ip = startedIP
		meta = instanceVMMetadata{
			Runtime:         d.rt.Name(),
			SnapshotRef:     startMeta.SnapshotRef,
			SharedRootfsRef: startMeta.SharedRootfsRef,
			CloneSourceVMID: pguuid.ToPgtype(startMeta.CloneSourceVMID),
		}
	default:
		startedIP, err := d.rt.Start(ctx, startInst)
		if err != nil {
			_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "failed",
			})
			return fmt.Errorf("start instance: %w", err)
		}
		ip = startedIP
		meta = instanceVMMetadata{Runtime: d.rt.Name()}
		d.fillLiveMigrationMetadata(ctx, instID, &meta)
	}

	if requiresExternalHealthCheck(d.rt.Name()) && !d.waitHealthCheckLocalForwarded(ip, 90*time.Second) {
		_ = d.rt.Stop(ctx, instID)
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("health check failed")
	}

	vmID, err := d.persistInstanceVMMetadata(ctx, d.q, inst.ID, dep.ImageID, pguuid.FromPgtype(inst.ServerID), ip, 1, 512, env, meta)
	if err != nil {
		_ = d.rt.Stop(ctx, instID)
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("persist vm metadata: %w", err)
	}
	if persistentVolume != nil {
		if _, err := d.q.ProjectVolumeAttachVM(ctx, queries.ProjectVolumeAttachVMParams{
			ProjectID:    dep.ProjectID,
			ServerID:     inst.ServerID,
			AttachedVmID: pguuid.ToPgtype(vmID),
		}); err != nil {
			_ = d.rt.Stop(ctx, instID)
			_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "failed",
			})
			return fmt.Errorf("attach project volume to vm: %w", err)
		}
		d.publishProjectVolumeEvents(dep.ProjectID)
	}
	return nil
}

func (d *Deployer) countInstancesReady(ctx context.Context, instList []queries.DeploymentInstance) int {
	n := 0
	for _, inst := range instList {
		if !isActiveInstance(inst) {
			continue
		}
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

func (d *Deployer) countActiveInstances(instList []queries.DeploymentInstance) int {
	n := 0
	for _, inst := range instList {
		if isActiveInstance(inst) {
			n++
		}
	}
	return n
}

func (d *Deployer) countProvisionableInstances(instList []queries.DeploymentInstance) int {
	n := 0
	for _, inst := range instList {
		if isActiveInstance(inst) || isWarmPoolInstance(inst) {
			n++
		}
	}
	return n
}

func (d *Deployer) retainedWarmPoolBudget() int {
	if d.rt != nil && d.rt.Supports(runtime.CapabilitySuspendResume) {
		return 1
	}
	return 0
}

func (d *Deployer) pruneWarmPoolInstances(ctx context.Context, instList []queries.DeploymentInstance, keep int) error {
	var warmPool []queries.DeploymentInstance
	for _, inst := range instList {
		if isWarmPoolInstance(inst) {
			warmPool = append(warmPool, inst)
		}
	}
	if len(warmPool) <= keep {
		return nil
	}
	slices.SortFunc(warmPool, func(a, b queries.DeploymentInstance) int {
		if a.CreatedAt.Time.Equal(b.CreatedAt.Time) {
			return 0
		}
		if a.CreatedAt.Time.After(b.CreatedAt.Time) {
			return -1
		}
		return 1
	})
	for _, inst := range warmPool[keep:] {
		d.deleteInstancePermanently(ctx, inst)
	}
	return nil
}

func (d *Deployer) templateSourceForDeployment(ctx context.Context, instList []queries.DeploymentInstance, imageID pgtype.UUID) (string, pgtype.UUID) {
	for _, inst := range instList {
		if !inst.VmID.Valid {
			continue
		}
		vm, err := d.q.VMFirstByID(ctx, inst.VmID)
		if err != nil || vm.DeletedAt.Valid || !vm.SnapshotRef.Valid {
			continue
		}
		if imageID.Valid && vm.ImageID != imageID {
			continue
		}
		if vm.ServerID.Valid && pguuid.FromPgtype(vm.ServerID) != d.serverID {
			continue
		}
		return vm.SnapshotRef.String, vm.ID
	}
	return "", pgtype.UUID{}
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
	if vm.ServerID.Valid && pguuid.FromPgtype(vm.ServerID) == d.serverID {
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

// waitHealthCheckLocalForwarded retries until the workload listens (OCI / VM
// publish can take tens of seconds after the parent process returns).
func (d *Deployer) waitHealthCheckLocalForwarded(runtimeURL string, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		if d.healthCheckLocalForwarded(runtimeURL) {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func requiresExternalHealthCheck(runtimeName string) bool {
	return runtimeName != "apple-vz"
}

func shouldKeepRunningVM(vm queries.Vm, runtimeName string, hostHealthCheckOK bool) bool {
	if vm.DeletedAt.Valid || vm.Status != "running" {
		return false
	}
	if requiresExternalHealthCheck(runtimeName) && !hostHealthCheckOK {
		return false
	}
	return true
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
