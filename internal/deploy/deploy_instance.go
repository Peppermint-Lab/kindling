package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

// Restart budget constants: max restarts per time window before circuit breaker trips.
const maxRestartCount = 3               // max restarts within the budget window before giving up
const restartBudgetSeconds = 5 * 60    // 5-minute window for restart budget

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

	// Circuit breaker: if the deployment's circuit is broken, stop retrying.
	if dep.CircuitBroken {
		logger.Info("circuit breaker open for deployment, skipping instance retry",
			"instance_id", pguuid.FromPgtype(inst.ID))
		return nil
	}

	// Restart budget: if the instance has exceeded the restart budget, trip the circuit
	// breaker for this deployment and stop retrying.
	if inst.RestartCount >= maxRestartCount && inst.LastRestartAt.Valid {
		windowStart := inst.LastRestartAt.Time.Add(time.Duration(restartBudgetSeconds) * time.Second)
		if time.Now().Before(windowStart) {
			logger.Warn("restart budget exceeded, tripping circuit breaker",
				"instance_id", pguuid.FromPgtype(inst.ID),
				"restart_count", inst.RestartCount,
				"last_restart_at", inst.LastRestartAt.Time)
			if _, err := d.q.DeploymentCircuitBreak(ctx, dep.ID); err != nil {
				logger.Warn("failed to trip circuit breaker", "deployment_id", dep.ID, "error", err)
			}
			return nil
		}
		// Budget window has passed; reset and allow retry.
	}

	inst, err := d.reconcileInstanceServerAssignment(ctx, dep, inst, persistentVolume, logger)
	if err != nil {
		return err
	}

	if inst.VmID.Valid {
		if done, err := d.tryResumeFromSuspended(ctx, dep, inst, persistentVolume, logger); err != nil || done {
			return err
		}
	}

	if inst.Status == "running" && inst.VmID.Valid {
		if d.isRunningInstanceHealthy(ctx, inst) {
			return nil
		}
	}

	inst, err = d.resetStaleInstance(ctx, dep, inst, persistentVolume)
	if err != nil {
		return err
	}

	inst, err = d.assignServerIfNeeded(ctx, dep, inst, persistentVolume)
	if err != nil {
		return err
	}

	if pguuid.FromPgtype(inst.ServerID) != d.serverID {
		return nil
	}

	return d.startNewInstance(ctx, dep, inst, imageRef, env, templateRef, templateSourceVMID, persistentVolume, logger)
}

// reconcileInstanceServerAssignment handles server pinning and draining logic
// for an instance that already has a server assigned.
func (d *Deployer) reconcileInstanceServerAssignment(
	ctx context.Context,
	dep queries.Deployment,
	inst queries.DeploymentInstance,
	persistentVolume *runtime.PersistentVolumeMount,
	logger *slog.Logger,
) (queries.DeploymentInstance, error) {
	if !inst.ServerID.Valid {
		return inst, nil
	}

	if persistentVolume != nil {
		projectVolume, _, err := d.projectVolumeForDeployment(ctx, dep)
		if err != nil {
			return inst, fmt.Errorf("load project volume: %w", err)
		}
		if projectVolume != nil && projectVolume.ServerID.Valid && inst.ServerID != projectVolume.ServerID {
			if _, err := d.q.DeploymentInstanceResetForRetry(ctx, inst.ID); err != nil {
				return inst, fmt.Errorf("reset instance for pinned volume server: %w", err)
			}
			inst, err = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
			if err != nil {
				return inst, fmt.Errorf("re-fetch instance after volume server reset: %w", err)
			}
			return inst, nil
		}
	}

	srv, err := d.q.ServerFindByID(ctx, inst.ServerID)
	if err != nil {
		return inst, nil
	}
	if srv.Status != "draining" {
		return inst, nil
	}

	if isWarmPoolInstance(inst) {
		d.deleteInstancePermanently(ctx, inst)
		return inst, nil
	}
	if inst.VmID.Valid {
		if vm, vmErr := d.q.VMFirstByID(ctx, inst.VmID); vmErr == nil && vm.Status == vmStatusSuspended {
			d.deleteInstancePermanently(ctx, inst)
			return inst, nil
		}
	}
	if inst.Status != "running" || !inst.VmID.Valid {
		if _, err := d.q.DeploymentInstanceResetForRetry(ctx, inst.ID); err != nil {
			return inst, fmt.Errorf("release instance from draining server: %w", err)
		}
		inst, err = d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
		if err != nil {
			return inst, fmt.Errorf("re-fetch instance after draining server release: %w", err)
		}
	}
	return inst, nil
}

// tryResumeFromSuspended attempts to resume a suspended VM and returns (done, err).
// done=true means the instance was handled (either resumed or reset for retry).
func (d *Deployer) tryResumeFromSuspended(
	ctx context.Context,
	dep queries.Deployment,
	inst queries.DeploymentInstance,
	persistentVolume *runtime.PersistentVolumeMount,
	logger *slog.Logger,
) (bool, error) {
	vm, err := d.q.VMFirstByID(ctx, inst.VmID)
	if err != nil || vm.Status != vmStatusSuspended || (!isActiveInstance(inst) && !isWarmPoolInstance(inst)) {
		return false, nil
	}
	if pguuid.FromPgtype(inst.ServerID) != d.serverID {
		return true, nil
	}
	if isWarmPoolInstance(inst) {
		updated, updateErr := d.q.DeploymentInstanceUpdateRole(ctx, queries.DeploymentInstanceUpdateRoleParams{
			ID:   inst.ID,
			Role: deploymentInstanceRoleActive,
		})
		if updateErr != nil {
			return false, fmt.Errorf("promote warm pool instance: %w", updateErr)
		}
		inst = updated
	}
	if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
		ID:     inst.ID,
		Status: "starting",
	}); err != nil {
		return false, fmt.Errorf("mark resuming: %w", err)
	}
	startedAt := time.Now()
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
				return false, fmt.Errorf("reset stale suspended instance: %w", prepErr)
			}
			return true, nil
		}
		return false, fmt.Errorf("resume instance: %w", err)
	}
	addr, port, parseErr := parseRuntimeAddress(ip)
	if parseErr == nil {
		if _, err := d.q.VMUpdateRuntimeAddress(ctx, queries.VMUpdateRuntimeAddressParams{
			ID:        inst.VmID,
			IpAddress: addr,
			Port:      pgtype.Int4{Int32: int32(port), Valid: true},
		}); err != nil {
			return false, fmt.Errorf("update resumed vm runtime address: %w", err)
		}
	}
	logger.Info("instance runtime ready", "instance_id", pguuid.FromPgtype(inst.ID), "mode", launchModeResume, "runtime", d.rt.Name(), "duration_ms", time.Since(startedAt).Milliseconds())
	if requiresExternalHealthCheck(d.rt.Name()) && !d.waitHealthCheckLocalForwarded(ip, healthCheckTimeout) {
		return false, fmt.Errorf("health check failed after resume")
	}
	logger.Info("instance health check passed", "instance_id", pguuid.FromPgtype(inst.ID), "mode", launchModeResume, "runtime", d.rt.Name(), "duration_ms", time.Since(startedAt).Milliseconds())
	if _, err := d.q.VMUpdateLifecycleMetadata(ctx, queries.VMUpdateLifecycleMetadataParams{
		ID:              inst.VmID,
		Status:          "running",
		SnapshotRef:     vm.SnapshotRef,
		SharedRootfsRef: vm.SharedRootfsRef,
		CloneSourceVmID: vm.CloneSourceVmID,
	}); err != nil {
		return false, fmt.Errorf("mark resumed vm running: %w", err)
	}
	if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
		ID:     inst.ID,
		Status: "running",
	}); err != nil {
		return false, fmt.Errorf("mark resumed instance running: %w", err)
	}
	return true, nil
}

// isRunningInstanceHealthy checks if a running instance is still healthy.
func (d *Deployer) isRunningInstanceHealthy(ctx context.Context, inst queries.DeploymentInstance) bool {
	vm, err := d.q.VMFirstByID(ctx, inst.VmID)
	if err != nil {
		return false
	}
	port := 3000
	if vm.Port.Valid {
		port = int(vm.Port.Int32)
	}
	hostHealthCheckOK := true
	if d.rt != nil && requiresExternalHealthCheck(d.rt.Name()) {
		hostHealthCheckOK = d.healthCheckVMFromHost(vm, port)
	}
	return shouldKeepRunningVM(vm, d.rt.Name(), hostHealthCheckOK)
}

// resetStaleInstance cleans up a stale VM/instance and prepares it for retry.
func (d *Deployer) resetStaleInstance(
	ctx context.Context,
	dep queries.Deployment,
	inst queries.DeploymentInstance,
	persistentVolume *runtime.PersistentVolumeMount,
) (queries.DeploymentInstance, error) {
	if (!inst.VmID.Valid || isWarmPoolInstance(inst)) && inst.Status != "failed" {
		return inst, nil
	}
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
		return inst, fmt.Errorf("prepare retry: %w", err)
	}
	refetched, refetchErr := d.q.DeploymentInstanceFirstByID(ctx, inst.ID)
	if refetchErr != nil {
		return inst, fmt.Errorf("re-fetch instance after prepare retry: %w", refetchErr)
	}
	return refetched, nil
}

// assignServerIfNeeded picks a server for an unassigned instance.
func (d *Deployer) assignServerIfNeeded(
	ctx context.Context,
	dep queries.Deployment,
	inst queries.DeploymentInstance,
	persistentVolume *runtime.PersistentVolumeMount,
) (queries.DeploymentInstance, error) {
	if inst.ServerID.Valid {
		return inst, nil
	}
	var (
		srv queries.Server
		err error
	)
	if persistentVolume != nil {
		projectVolume, _, volumeErr := d.projectVolumeForDeployment(ctx, dep)
		if volumeErr != nil {
			return inst, fmt.Errorf("load project volume: %w", volumeErr)
		}
		if projectVolume == nil || !projectVolume.ServerID.Valid {
			return inst, fmt.Errorf("project volume server is not assigned")
		}
		srv, err = d.q.ServerFindByID(ctx, projectVolume.ServerID)
	} else {
		srv, err = d.q.ServerFindLeastLoaded(ctx)
	}
	if err != nil {
		return inst, fmt.Errorf("pick server: %w", err)
	}
	updated, err := d.q.DeploymentInstanceUpdateServer(ctx, queries.DeploymentInstanceUpdateServerParams{
		ID:       inst.ID,
		ServerID: srv.ID,
	})
	if err != nil {
		return inst, fmt.Errorf("assign server: %w", err)
	}
	return updated, nil
}

// startNewInstance provisions and starts a new VM for the given instance.
func (d *Deployer) startNewInstance(
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
	startedAt := time.Now()
	logger.Info("starting instance", "instance_id", instID, "image", imageRef, "runtime", d.rt.Name(), "mode", mode)

	ip, meta, err := d.launchInstance(ctx, startInst, templateRef, templateSourceVMID, inst, mode)
	if err != nil {
		return err
	}
	logger.Info("instance runtime ready", "instance_id", instID, "runtime", d.rt.Name(), "mode", mode, "duration_ms", time.Since(startedAt).Milliseconds())

	vmID, err := d.persistInstanceVMMetadata(ctx, d.q, inst.ID, dep.ImageID, pguuid.FromPgtype(inst.ServerID), "starting", "starting", ip, 1, 512, env, meta)
	if err != nil {
		_ = d.rt.Stop(ctx, instID)
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("persist vm metadata: %w", err)
	}
	if requiresExternalHealthCheck(d.rt.Name()) && !d.waitHealthCheckLocalForwarded(ip, healthCheckTimeout) {
		_ = d.rt.Stop(ctx, instID)
		_ = d.q.VMSoftDelete(ctx, pguuid.ToPgtype(vmID))
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("health check failed")
	}
	logger.Info("instance health check passed", "instance_id", instID, "runtime", d.rt.Name(), "mode", mode, "duration_ms", time.Since(startedAt).Milliseconds())
	if _, err := d.q.VMUpdateStatus(ctx, queries.VMUpdateStatusParams{
		ID:     pguuid.ToPgtype(vmID),
		Status: "running",
	}); err != nil {
		_ = d.rt.Stop(ctx, instID)
		_ = d.q.VMSoftDelete(ctx, pguuid.ToPgtype(vmID))
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("mark vm running: %w", err)
	}
	if _, err := d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
		ID:     inst.ID,
		Status: "running",
	}); err != nil {
		_ = d.rt.Stop(ctx, instID)
		_ = d.q.VMSoftDelete(ctx, pguuid.ToPgtype(vmID))
		_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
			ID:     inst.ID,
			Status: "failed",
		})
		return fmt.Errorf("mark deployment instance running: %w", err)
	}
	if persistentVolume != nil {
		if _, err := d.q.ProjectVolumeAttachVM(ctx, queries.ProjectVolumeAttachVMParams{
			ProjectID:    dep.ProjectID,
			ServerID:     inst.ServerID,
			AttachedVmID: pguuid.ToPgtype(vmID),
		}); err != nil {
			_ = d.rt.Stop(ctx, instID)
			_ = d.q.VMSoftDelete(ctx, pguuid.ToPgtype(vmID))
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

// launchInstance starts a VM using the appropriate launch mode (clone or cold).
func (d *Deployer) launchInstance(
	ctx context.Context,
	startInst runtime.Instance,
	templateRef string,
	templateSourceVMID pgtype.UUID,
	inst queries.DeploymentInstance,
	mode launchMode,
) (string, instanceVMMetadata, error) {
	switch {
	case mode == launchModeClone && d.rt.Supports(runtime.CapabilityWarmClone):
		startedIP, startMeta, err := d.rt.StartClone(ctx, startInst, templateRef, pguuid.FromPgtype(templateSourceVMID))
		if err != nil {
			if errors.Is(err, runtime.ErrInstanceNotRunning) {
				slog.Warn("warm clone state missing, falling back to cold start", "instance_id", startInst.ID, "template_ref", templateRef)
				startedIP, coldErr := d.rt.Start(ctx, startInst)
				if coldErr != nil {
					_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
						ID:     inst.ID,
						Status: "failed",
					})
					return "", instanceVMMetadata{}, fmt.Errorf("start clone fallback cold start: %w", coldErr)
				}
				meta := instanceVMMetadata{Runtime: d.rt.Name()}
				d.fillLiveMigrationMetadata(ctx, startInst.ID, &meta)
				return startedIP, meta, nil
			}
			_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "failed",
			})
			return "", instanceVMMetadata{}, fmt.Errorf("start clone: %w", err)
		}
		return startedIP, instanceVMMetadata{
			Runtime:         d.rt.Name(),
			SnapshotRef:     startMeta.SnapshotRef,
			SharedRootfsRef: startMeta.SharedRootfsRef,
			CloneSourceVMID: pguuid.ToPgtype(startMeta.CloneSourceVMID),
		}, nil

	default:
		startedIP, err := d.rt.Start(ctx, startInst)
		if err != nil {
			_, _ = d.q.DeploymentInstanceUpdateStatus(ctx, queries.DeploymentInstanceUpdateStatusParams{
				ID:     inst.ID,
				Status: "failed",
			})
			return "", instanceVMMetadata{}, fmt.Errorf("start instance: %w", err)
		}
		meta := instanceVMMetadata{Runtime: d.rt.Name()}
		d.fillLiveMigrationMetadata(ctx, startInst.ID, &meta)
		return startedIP, meta, nil
	}
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
