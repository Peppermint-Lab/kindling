package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type projectVolumeOperationRequest struct {
	BackupID       string `json:"backup_id,omitempty"`
	BackupKind     string `json:"backup_kind,omitempty"`
	TargetServerID string `json:"target_server_id,omitempty"`
	SourceServerID string `json:"source_server_id,omitempty"`
	Stage          string `json:"stage,omitempty"`
	DeleteAfter    bool   `json:"delete_after,omitempty"`
}

func (a *API) listProjectVolumeBackups(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	vol, err := a.q.ProjectVolumeFindByProjectID(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "project volume not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_volume_backups", err)
		return
	}
	backups, err := a.q.ProjectVolumeBackupFindByProjectID(r.Context(), projectID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_project_volume_backups", err)
		return
	}
	out := make([]projectVolumeBackupOut, 0, len(backups))
	for _, backup := range backups {
		if backup.ProjectVolumeID != vol.ID {
			continue
		}
		out = append(out, projectVolumeBackupToOut(backup))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) postProjectVolumeBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	vol, currentOp, ok := a.projectVolumeReadyForOperation(w, r, projectID, "backup")
	if !ok {
		_ = currentOp
		return
	}
	if !vol.ServerID.Valid {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", "volume is not pinned to a worker yet")
		return
	}
	if err := a.ensureVolumeBackupStoreConfigured(); err != nil {
		writeAPIError(w, http.StatusConflict, "backup_store_unavailable", err.Error())
		return
	}
	backup, err := a.q.ProjectVolumeBackupCreate(r.Context(), queries.ProjectVolumeBackupCreateParams{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectVolumeID: vol.ID,
		Kind:            "manual",
		Metadata:        []byte(`{}`),
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project_volume_backup", err)
		return
	}
	op, err := a.enqueueProjectVolumeOperation(r.Context(), vol, vol.ServerID, "backup", projectVolumeOperationRequest{
		BackupID:   pguuid.ToString(backup.ID),
		BackupKind: "manual",
	}, "backing_up")
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project_volume_backup", err)
		return
	}
	a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	writeJSON(w, http.StatusAccepted, projectVolumeOperationToOut(op))
}

func (a *API) postProjectVolumeRestore(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	vol, _, ok := a.projectVolumeReadyForOperation(w, r, projectID, "restore")
	if !ok {
		return
	}
	if err := a.ensureVolumeBackupStoreConfigured(); err != nil {
		writeAPIError(w, http.StatusConflict, "backup_store_unavailable", err.Error())
		return
	}
	var req struct {
		BackupID       string `json:"backup_id"`
		TargetServerID string `json:"target_server_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	backupID, err := uuid.Parse(strings.TrimSpace(req.BackupID))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "backup_id is required")
		return
	}
	backup, err := a.q.ProjectVolumeBackupFindByID(r.Context(), pgtype.UUID{Bytes: backupID, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "backup not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "restore_project_volume", err)
		return
	}
	if backup.ProjectVolumeID != vol.ID {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "backup does not belong to this project volume")
		return
	}
	targetServer, err := a.selectProjectVolumeTargetServer(r.Context(), strings.TrimSpace(req.TargetServerID), vol.ServerID, true)
	if err != nil {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", err.Error())
		return
	}
	op, err := a.enqueueProjectVolumeOperation(r.Context(), vol, targetServer.ID, "restore", projectVolumeOperationRequest{
		BackupID:       backupID.String(),
		TargetServerID: pguuid.ToString(targetServer.ID),
	}, "restoring")
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "restore_project_volume", err)
		return
	}
	a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	writeJSON(w, http.StatusAccepted, projectVolumeOperationToOut(op))
}

func (a *API) postProjectVolumeMove(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	vol, _, ok := a.projectVolumeReadyForOperation(w, r, projectID, "move")
	if !ok {
		return
	}
	if !vol.ServerID.Valid {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", "volume is not pinned to a worker yet")
		return
	}
	if err := a.ensureVolumeBackupStoreConfigured(); err != nil {
		writeAPIError(w, http.StatusConflict, "backup_store_unavailable", err.Error())
		return
	}
	var req struct {
		TargetServerID string `json:"target_server_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	targetServer, err := a.selectProjectVolumeTargetServer(r.Context(), strings.TrimSpace(req.TargetServerID), vol.ServerID, false)
	if err != nil {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", err.Error())
		return
	}
	if targetServer.ID == vol.ServerID {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "volume is already pinned to that server")
		return
	}
	op, err := a.enqueueProjectVolumeOperation(r.Context(), vol, vol.ServerID, "move", projectVolumeOperationRequest{
		TargetServerID: pguuid.ToString(targetServer.ID),
		SourceServerID: pguuid.ToString(vol.ServerID),
		Stage:          "upload",
	}, "restoring")
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "move_project_volume", err)
		return
	}
	a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	writeJSON(w, http.StatusAccepted, projectVolumeOperationToOut(op))
}

func (a *API) postProjectVolumeRepair(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, _, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}
	vol, _, ok := a.projectVolumeReadyForOperation(w, r, projectID, "repair")
	if !ok {
		return
	}
	if !vol.ServerID.Valid {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", "volume is not pinned to a worker yet")
		return
	}
	op, err := a.enqueueProjectVolumeOperation(r.Context(), vol, vol.ServerID, "repair", projectVolumeOperationRequest{}, "repairing")
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "repair_project_volume", err)
		return
	}
	a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	writeJSON(w, http.StatusAccepted, projectVolumeOperationToOut(op))
}

func (a *API) projectVolumeReadyForOperation(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID, action string) (queries.ProjectVolume, *queries.ProjectVolumeOperation, bool) {
	vol, err := a.q.ProjectVolumeFindByProjectID(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "project volume not found")
			return queries.ProjectVolume{}, nil, false
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, action+"_project_volume", err)
		return queries.ProjectVolume{}, nil, false
	}
	if vol.Status == "attached" || vol.AttachedVmID.Valid {
		writeAPIError(w, http.StatusConflict, "volume_attached", "detach the volume before starting this operation")
		return queries.ProjectVolume{}, nil, false
	}
	if isProjectVolumeTransitionalStatus(vol.Status) {
		writeAPIError(w, http.StatusConflict, "volume_busy", "another volume operation is already in progress")
		return queries.ProjectVolume{}, nil, false
	}
	if op, err := a.q.ProjectVolumeOperationFindCurrentByVolumeID(r.Context(), vol.ID); err == nil {
		writeAPIError(w, http.StatusConflict, "volume_busy", "another volume operation is already in progress")
		return queries.ProjectVolume{}, &op, false
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, action+"_project_volume", err)
		return queries.ProjectVolume{}, nil, false
	}
	return vol, nil, true
}

func (a *API) enqueueProjectVolumeOperation(ctx context.Context, vol queries.ProjectVolume, serverID pgtype.UUID, kind string, req projectVolumeOperationRequest, volumeStatus string) (queries.ProjectVolumeOperation, error) {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return queries.ProjectVolumeOperation{}, err
	}
	op, err := a.q.ProjectVolumeOperationCreate(ctx, queries.ProjectVolumeOperationCreateParams{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectVolumeID: vol.ID,
		ServerID:        serverID,
		Kind:            kind,
		RequestMetadata: reqBytes,
	})
	if err != nil {
		return queries.ProjectVolumeOperation{}, err
	}
	_, err = a.q.ProjectVolumeUpdateStatusAndHealth(ctx, queries.ProjectVolumeUpdateStatusAndHealthParams{
		ProjectID: vol.ProjectID,
		Status:    volumeStatus,
		Health:    vol.Health,
		LastError: "",
	})
	return op, err
}

func (a *API) ensureVolumeBackupStoreConfigured() error {
	if a == nil || a.cfg == nil {
		return errors.New("volume backup object storage is not configured")
	}
	snap := a.cfg.Snapshot()
	if snap == nil {
		return errors.New("volume backup object storage is not configured")
	}
	if strings.TrimSpace(snap.VolumeBackupS3Bucket) == "" ||
		strings.TrimSpace(snap.VolumeBackupS3Region) == "" ||
		strings.TrimSpace(snap.VolumeBackupS3AccessKeyID) == "" ||
		strings.TrimSpace(snap.VolumeBackupS3SecretAccessKey) == "" {
		return errors.New("volume backup object storage is not configured")
	}
	return nil
}

func (a *API) selectProjectVolumeTargetServer(ctx context.Context, requestedID string, currentServerID pgtype.UUID, preferCurrent bool) (queries.Server, error) {
	servers, err := a.activeCloudHypervisorWorkers(ctx)
	if err != nil {
		return queries.Server{}, err
	}
	if len(servers) == 0 {
		return queries.Server{}, errors.New("no active cloud-hypervisor worker is available")
	}
	if requestedID != "" {
		targetID, err := uuid.Parse(requestedID)
		if err != nil {
			return queries.Server{}, errors.New("target_server_id must be a valid UUID")
		}
		for _, server := range servers {
			if server.ID.Valid && uuid.UUID(server.ID.Bytes) == targetID {
				return server, nil
			}
		}
		return queries.Server{}, errors.New("target server is not an active cloud-hypervisor worker")
	}
	if preferCurrent && currentServerID.Valid {
		for _, server := range servers {
			if server.ID == currentServerID {
				return server, nil
			}
		}
	}
	if !preferCurrent && currentServerID.Valid && len(servers) > 1 && servers[0].ID == currentServerID {
		return servers[1], nil
	}
	return servers[0], nil
}

func (a *API) activeCloudHypervisorWorkers(ctx context.Context) ([]queries.Server, error) {
	servers, err := a.q.ServerFindAll(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := a.q.ServerComponentStatusFindAll(ctx)
	if err != nil {
		return nil, err
	}
	runtimeByServer := make(map[[16]byte]string)
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
	active := make([]queries.Server, 0, len(servers))
	cutoff := time.Now().UTC().Add(-3 * time.Minute)
	for _, server := range servers {
		if !server.ID.Valid || server.Status != "active" {
			continue
		}
		if !server.LastHeartbeatAt.Valid || server.LastHeartbeatAt.Time.Before(cutoff) {
			continue
		}
		if runtimeByServer[server.ID.Bytes] != "cloud-hypervisor" {
			continue
		}
		active = append(active, server)
	}
	slices.SortFunc(active, func(left, right queries.Server) int {
		loadA, _ := a.q.DeploymentInstanceActiveCountByServerID(ctx, left.ID)
		loadB, _ := a.q.DeploymentInstanceActiveCountByServerID(ctx, right.ID)
		if loadA != loadB {
			if loadA < loadB {
				return -1
			}
			return 1
		}
		return strings.Compare(left.Hostname, right.Hostname)
	})
	return active, nil
}

func (a *API) publishProjectVolumeTopics(projectID uuid.UUID) {
	if a.dashboardEvents == nil {
		return
	}
	a.dashboardEvents.PublishMany(
		TopicProject(projectID),
		TopicProjectDeployments(projectID),
		TopicDeployments,
		TopicServers,
	)
}
