package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (d *Deployer) serverStatusByIDs(ctx context.Context, instList []queries.DeploymentInstance) (map[uuid.UUID]string, error) {
	m := make(map[uuid.UUID]string)
	seen := make(map[uuid.UUID]struct{})
	for _, inst := range instList {
		if !inst.ServerID.Valid {
			continue
		}
		id := uuidFromPgtype(inst.ServerID)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		srv, err := d.q.ServerFindByID(ctx, inst.ServerID)
		if err != nil {
			return nil, fmt.Errorf("server %s: %w", id, err)
		}
		m[id] = srv.Status
	}
	return m, nil
}

func (d *Deployer) countInstancesOnDrainingServers(instList []queries.DeploymentInstance, statusMap map[uuid.UUID]string) int {
	n := 0
	for _, inst := range instList {
		if !inst.ServerID.Valid {
			continue
		}
		if statusMap[uuidFromPgtype(inst.ServerID)] == "draining" {
			n++
		}
	}
	return n
}

func (d *Deployer) countReadyOffDrainingServers(ctx context.Context, instList []queries.DeploymentInstance, statusMap map[uuid.UUID]string) int {
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
		if d.rt != nil && requiresExternalHealthCheck(d.rt.Name()) && !d.healthCheckVMFromHost(vm, port) {
			continue
		}
		if inst.ServerID.Valid {
			sid := uuidFromPgtype(inst.ServerID)
			if statusMap[sid] == "draining" {
				continue
			}
		}
		n++
	}
	return n
}

func (d *Deployer) scaleDownExcess(ctx context.Context, deploymentID pgtype.UUID, instList []queries.DeploymentInstance, target int, initStatus map[uuid.UUID]string, logger *slog.Logger) ([]queries.DeploymentInstance, error) {
	list := instList
	statusMap := initStatus
	var err error
	for len(list) > target {
		if statusMap == nil {
			statusMap, err = d.serverStatusByIDs(ctx, list)
			if err != nil {
				return nil, err
			}
		}
		sorted := slices.Clone(list)
		slices.SortFunc(sorted, func(a, b queries.DeploymentInstance) int {
			if a.CreatedAt.Time.Equal(b.CreatedAt.Time) {
				return 0
			}
			if a.CreatedAt.Time.After(b.CreatedAt.Time) {
				return -1
			}
			return 1
		})
		var victim queries.DeploymentInstance
		found := false
		for _, inst := range sorted {
			drain := inst.ServerID.Valid && statusMap[uuidFromPgtype(inst.ServerID)] == "draining"
			if !drain {
				victim = inst
				found = true
				break
			}
		}
		if !found && len(sorted) > 0 {
			victim = sorted[0]
		}
		if len(sorted) == 0 {
			break
		}
		logger.Info("scaling down excess instance", "instance_id", uuidFromPgtype(victim.ID), "have", len(list), "target", target)
		d.cleanupInstance(ctx, victim)
		list, err = d.q.DeploymentInstanceFindByDeploymentID(ctx, deploymentID)
		if err != nil {
			return nil, err
		}
		statusMap = nil
	}
	return list, nil
}

func (d *Deployer) removeInstancesOnDrainingServers(ctx context.Context, depID pgtype.UUID, instList []queries.DeploymentInstance, statusMap map[uuid.UUID]string, desired int32, logger *slog.Logger) ([]queries.DeploymentInstance, error) {
	if d.countReadyOffDrainingServers(ctx, instList, statusMap) < int(desired) {
		return instList, nil
	}
	var toRemove []queries.DeploymentInstance
	for _, inst := range instList {
		if !inst.ServerID.Valid {
			continue
		}
		if statusMap[uuidFromPgtype(inst.ServerID)] != "draining" {
			continue
		}
		toRemove = append(toRemove, inst)
	}
	if len(toRemove) == 0 {
		return instList, nil
	}
	serversToNotify := make(map[uuid.UUID]struct{})
	for _, inst := range toRemove {
		if inst.ServerID.Valid {
			serversToNotify[uuidFromPgtype(inst.ServerID)] = struct{}{}
		}
		logger.Info("retiring instance from draining server", "instance_id", uuidFromPgtype(inst.ID))
		d.cleanupInstance(ctx, inst)
	}
	list, err := d.q.DeploymentInstanceFindByDeploymentID(ctx, depID)
	if err != nil {
		return nil, err
	}
	if d.serverScheduler != nil {
		for sid := range serversToNotify {
			d.serverScheduler.ScheduleNow(sid)
		}
	}
	return list, nil
}
