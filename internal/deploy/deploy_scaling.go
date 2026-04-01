package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

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
		return fmt.Errorf("list instances after scale down: %w", err)
	}
	return d.pruneWarmPoolInstances(ctx, refreshed, d.retainedWarmPoolBudget(ctx, instList[0].DeploymentID))
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
			return fmt.Errorf("list instances in tx: %w", err)
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
				return fmt.Errorf("list instances after create in tx: %w", err)
			}
		}
		return tx.Commit(ctx)
	}
	rows, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
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
			return fmt.Errorf("list instances after create: %w", err)
		}
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
		hostHealthCheckOK := true
		if requiresExternalHealthCheck(d.rt.Name()) {
			hostHealthCheckOK = d.healthCheckVMFromHost(vm, port)
		}
		if !shouldTreatRunningInstanceAsHealthy(inst, vm, d.rt.Name(), hostHealthCheckOK, d.serverID, d.localRuntimeHealthy(ctx, inst)) {
			continue
		}
		n++
	}
	return n
}

func (d *Deployer) countActiveInstances(instList []queries.DeploymentInstance) int {
	return activeInstanceCount(instList)
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

func warmPoolPromotionCandidates(instList []queries.DeploymentInstance, desired int) []pgtype.UUID {
	deficit := desired - activeInstanceCount(instList)
	if deficit <= 0 {
		return nil
	}
	candidates := make([]pgtype.UUID, 0, deficit)
	for _, inst := range instList {
		if deficit == 0 {
			break
		}
		if !isWarmPoolInstance(inst) {
			continue
		}
		candidates = append(candidates, inst.ID)
		deficit--
	}
	return candidates
}

func activeInstanceCount(instList []queries.DeploymentInstance) int {
	n := 0
	for _, inst := range instList {
		if isActiveInstance(inst) {
			n++
		}
	}
	return n
}

func (d *Deployer) activateWarmPoolInstances(ctx context.Context, instList []queries.DeploymentInstance, desired int) ([]queries.DeploymentInstance, error) {
	candidates := warmPoolPromotionCandidates(instList, desired)
	if len(candidates) == 0 {
		return instList, nil
	}
	updated := make(map[pgtype.UUID]queries.DeploymentInstance, len(candidates))
	for _, instID := range candidates {
		inst, err := d.q.DeploymentInstanceUpdateRole(ctx, queries.DeploymentInstanceUpdateRoleParams{
			ID:   instID,
			Role: deploymentInstanceRoleActive,
		})
		if err != nil {
			return nil, fmt.Errorf("promote warm pool instance: %w", err)
		}
		updated[instID] = inst
	}
	refreshed := slices.Clone(instList)
	for i, inst := range refreshed {
		if next, ok := updated[inst.ID]; ok {
			refreshed[i] = next
		}
	}
	return refreshed, nil
}

const warmPoolExtraRetentionAfterTraffic = 15 * time.Minute

func (d *Deployer) retainedWarmPoolBudgetBase() int {
	if d.rt != nil && d.rt.Supports(runtime.CapabilitySuspendResume) {
		return 1
	}
	return 0
}

func (d *Deployer) retainedWarmPoolBudget(ctx context.Context, deploymentID pgtype.UUID) int {
	base := d.retainedWarmPoolBudgetBase()
	if base == 0 || !deploymentID.Valid {
		return base
	}
	dep, err := d.q.DeploymentFirstByID(ctx, deploymentID)
	if err != nil {
		return base
	}
	proj, err := d.q.ProjectFirstByID(ctx, dep.ProjectID)
	if err != nil {
		return base
	}
	if proj.LastRequestAt.Valid && warmPoolExtraRetentionEligible(proj.LastRequestAt.Time.UTC(), time.Now().UTC()) {
		return base + 1
	}
	return base
}

func warmPoolExtraRetentionEligible(lastRequestAt, now time.Time) bool {
	age := now.Sub(lastRequestAt)
	return age >= 0 && age < warmPoolExtraRetentionAfterTraffic
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
