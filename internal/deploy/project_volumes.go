package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
)

type projectVolumeUnavailableError struct {
	message string
}

func (e *projectVolumeUnavailableError) Error() string {
	return e.message
}

func isProjectVolumeUnavailableError(err error) bool {
	var target *projectVolumeUnavailableError
	return errors.As(err, &target)
}

func projectVolumeRetryDelay(err error) time.Duration {
	if isProjectVolumeUnavailableError(err) {
		return 30 * time.Second
	}
	return 5 * time.Second
}

func projectVolumeServerStatusMessage(serverID pgtype.UUID, status string) string {
	return fmt.Sprintf("pinned server %s is %s", uuidFromPgtype(serverID), status)
}

func (d *Deployer) projectVolumeForProject(ctx context.Context, projectID pgtype.UUID) (*queries.ProjectVolume, *runtime.PersistentVolumeMount, error) {
	vol, err := d.q.ProjectVolumeFindByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return &vol, persistentVolumeMountFromRow(vol), nil
}

func (d *Deployer) publishProjectVolumeEvents(projectID pgtype.UUID) {
	if d.publishProjectEvents != nil && projectID.Valid {
		d.publishProjectEvents(uuidFromPgtype(projectID))
	}
	if d.publishServerEvents != nil {
		d.publishServerEvents()
	}
}

func persistentVolumeMountFromRow(vol queries.ProjectVolume) *runtime.PersistentVolumeMount {
	return &runtime.PersistentVolumeMount{
		ID:         uuidFromPgtype(vol.ID),
		HostPath:   runtime.PersistentVolumePath(uuidFromPgtype(vol.ID)),
		MountPath:  vol.MountPath,
		SizeGB:     int(vol.SizeGb),
		Filesystem: vol.Filesystem,
	}
}

func (d *Deployer) ensureProjectVolumeServer(ctx context.Context, vol queries.ProjectVolume) (queries.ProjectVolume, error) {
	if vol.ServerID.Valid {
		srv, err := d.q.ServerFindByID(ctx, vol.ServerID)
		if err != nil {
			return d.markProjectVolumeUnavailable(ctx, vol.ProjectID, fmt.Sprintf("pinned server lookup failed: %v", err))
		}
		if srv.Status != "active" {
			return d.markProjectVolumeUnavailable(ctx, vol.ProjectID, projectVolumeServerStatusMessage(srv.ID, srv.Status))
		}
		if !d.serverSupportsCloudHypervisor(ctx, srv.ID) {
			return d.markProjectVolumeUnavailable(ctx, vol.ProjectID, fmt.Sprintf("pinned server %s is not a cloud-hypervisor worker", uuidFromPgtype(srv.ID)))
		}
		if vol.Status == "unavailable" && !vol.AttachedVmID.Valid {
			updated, err := d.q.ProjectVolumeUpdateStatus(ctx, queries.ProjectVolumeUpdateStatusParams{
				ProjectID: vol.ProjectID,
				Status:    "available",
				LastError: "",
			})
			if err == nil {
				d.publishProjectVolumeEvents(vol.ProjectID)
				return updated, nil
			}
		}
		return vol, nil
	}

	srv, err := d.findLeastLoadedCloudHypervisorServer(ctx)
	if err != nil {
		return d.markProjectVolumeUnavailable(ctx, vol.ProjectID, "no active cloud-hypervisor worker is available")
	}
	updated, err := d.q.ProjectVolumeAssignServer(ctx, queries.ProjectVolumeAssignServerParams{
		ProjectID: vol.ProjectID,
		ServerID:  srv.ID,
	})
	if err != nil {
		return vol, err
	}
	d.publishProjectVolumeEvents(vol.ProjectID)
	updated, err = d.q.ProjectVolumeUpdateStatus(ctx, queries.ProjectVolumeUpdateStatusParams{
		ProjectID: vol.ProjectID,
		Status:    "available",
		LastError: "",
	})
	if err != nil {
		return updated, err
	}
	d.publishProjectVolumeEvents(vol.ProjectID)
	return updated, nil
}

func (d *Deployer) markProjectVolumeUnavailable(ctx context.Context, projectID pgtype.UUID, message string) (queries.ProjectVolume, error) {
	message = strings.TrimSpace(message)
	vol, err := d.q.ProjectVolumeFindByProjectID(ctx, projectID)
	if err != nil {
		return queries.ProjectVolume{}, err
	}
	if vol.AttachedVmID.Valid {
		vol, err = d.q.ProjectVolumeDetachVM(ctx, queries.ProjectVolumeDetachVMParams{
			ProjectID: projectID,
			Status:    "unavailable",
			LastError: message,
		})
	} else {
		vol, err = d.q.ProjectVolumeUpdateStatus(ctx, queries.ProjectVolumeUpdateStatusParams{
			ProjectID: projectID,
			Status:    "unavailable",
			LastError: message,
		})
	}
	if err != nil {
		return queries.ProjectVolume{}, err
	}
	d.publishProjectVolumeEvents(projectID)
	return vol, &projectVolumeUnavailableError{message: message}
}

func (d *Deployer) serverSupportsCloudHypervisor(ctx context.Context, serverID pgtype.UUID) bool {
	rows, err := d.q.ServerComponentStatusFindByServerID(ctx, serverID)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Component != "worker" || row.Status != "healthy" {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			continue
		}
		if runtimeName, _ := meta["runtime"].(string); runtimeName == "cloud-hypervisor" {
			return true
		}
	}
	return false
}

func (d *Deployer) findLeastLoadedCloudHypervisorServer(ctx context.Context) (queries.Server, error) {
	servers, err := d.q.ServerFindAll(ctx)
	if err != nil {
		return queries.Server{}, err
	}
	statuses, err := d.q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return queries.Server{}, err
	}
	runtimeByServer := make(map[uuid.UUID]string)
	for _, row := range statuses {
		if row.Component != "worker" || row.Status != "healthy" || !row.ServerID.Valid {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal(row.Metadata, &meta); err != nil {
			continue
		}
		if runtimeName, _ := meta["runtime"].(string); runtimeName != "" {
			runtimeByServer[row.ServerID.Bytes] = runtimeName
		}
	}
	type candidate struct {
		server queries.Server
		load   int64
	}
	candidates := make([]candidate, 0, len(servers))
	cutoff := time.Now().UTC().Add(-3 * time.Minute)
	for _, server := range servers {
		if server.Status != "active" || !server.ID.Valid {
			continue
		}
		if !server.LastHeartbeatAt.Valid || server.LastHeartbeatAt.Time.Before(cutoff) {
			continue
		}
		if runtimeByServer[server.ID.Bytes] != "cloud-hypervisor" {
			continue
		}
		load, err := d.q.DeploymentInstanceActiveCountByServerID(ctx, server.ID)
		if err != nil {
			return queries.Server{}, err
		}
		candidates = append(candidates, candidate{server: server, load: load})
	}
	if len(candidates) == 0 {
		return queries.Server{}, pgx.ErrNoRows
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.load != b.load {
			if a.load < b.load {
				return -1
			}
			return 1
		}
		if a.server.LastHeartbeatAt.Time.After(b.server.LastHeartbeatAt.Time) {
			return -1
		}
		if a.server.LastHeartbeatAt.Time.Before(b.server.LastHeartbeatAt.Time) {
			return 1
		}
		return strings.Compare(a.server.Hostname, b.server.Hostname)
	})
	return candidates[0].server, nil
}

func (d *Deployer) detachProjectVolumeIfAttached(ctx context.Context, projectID, vmID pgtype.UUID, status, lastError string) error {
	if !projectID.Valid || !vmID.Valid {
		return nil
	}
	vol, err := d.q.ProjectVolumeFindByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !vol.AttachedVmID.Valid || vol.AttachedVmID != vmID {
		return nil
	}
	_, err = d.q.ProjectVolumeDetachVM(ctx, queries.ProjectVolumeDetachVMParams{
		ProjectID: projectID,
		Status:    status,
		LastError: lastError,
	})
	if err == nil {
		d.publishProjectVolumeEvents(projectID)
	}
	return err
}

func (d *Deployer) detachProjectVolumeForInstance(ctx context.Context, inst queries.DeploymentInstance, status, lastError string) error {
	if !inst.VmID.Valid {
		return nil
	}
	dep, err := d.q.DeploymentFirstByID(ctx, inst.DeploymentID)
	if err != nil {
		return err
	}
	return d.detachProjectVolumeIfAttached(ctx, dep.ProjectID, inst.VmID, status, lastError)
}

func (d *Deployer) markProjectVolumeUnavailableForInstance(ctx context.Context, inst queries.DeploymentInstance, serverStatus string) (bool, error) {
	if !inst.ServerID.Valid {
		return false, nil
	}
	dep, err := d.q.DeploymentFirstByID(ctx, inst.DeploymentID)
	if err != nil {
		return false, err
	}
	vol, err := d.q.ProjectVolumeFindByProjectID(ctx, dep.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !vol.ServerID.Valid || vol.ServerID != inst.ServerID {
		return false, nil
	}
	_, err = d.markProjectVolumeUnavailable(ctx, dep.ProjectID, projectVolumeServerStatusMessage(inst.ServerID, serverStatus))
	return true, err
}

func (d *Deployer) stopOldDeploymentsForVolume(ctx context.Context, current queries.Deployment, logger *slog.Logger) (bool, error) {
	old, err := d.q.DeploymentFindRunningAndOlder(ctx, queries.DeploymentFindRunningAndOlderParams{
		ProjectID: current.ProjectID,
		ID:        current.ID,
	})
	if err != nil || len(old) == 0 {
		return false, err
	}
	logger.Info("stopping old deployments before volume attach", "count", len(old))
	for _, dep := range old {
		instList, listErr := d.q.DeploymentInstanceFindByDeploymentID(ctx, dep.ID)
		if listErr != nil {
			return true, listErr
		}
		for _, inst := range instList {
			d.deleteInstancePermanently(ctx, inst)
		}
		if dep.VmID.Valid {
			_ = d.q.VMSoftDelete(ctx, dep.VmID)
		}
		if err := d.q.DeploymentMarkStopped(ctx, dep.ID); err != nil {
			return true, err
		}
	}
	return true, nil
}
