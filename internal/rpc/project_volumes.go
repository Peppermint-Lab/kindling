package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type projectVolumeBackupPolicyOut struct {
	Schedule               string `json:"schedule"`
	RetentionCount         int32  `json:"retention_count"`
	PreDeleteBackupEnabled bool   `json:"pre_delete_backup_enabled"`
}

type projectVolumeOperationOut struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Status         string  `json:"status"`
	ServerID       *string `json:"server_id,omitempty"`
	TargetServerID *string `json:"target_server_id,omitempty"`
	BackupID       *string `json:"backup_id,omitempty"`
	Error          string  `json:"error,omitempty"`
	StartedAt      *string `json:"started_at,omitempty"`
	CompletedAt    *string `json:"completed_at,omitempty"`
	FailedAt       *string `json:"failed_at,omitempty"`
	CreatedAt      *string `json:"created_at,omitempty"`
	UpdatedAt      *string `json:"updated_at,omitempty"`
}

type projectVolumeBackupOut struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	StorageURL  string  `json:"storage_url,omitempty"`
	SizeBytes   int64   `json:"size_bytes"`
	Error       string  `json:"error,omitempty"`
	StartedAt   *string `json:"started_at,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
	FailedAt    *string `json:"failed_at,omitempty"`
	CreatedAt   *string `json:"created_at,omitempty"`
	UpdatedAt   *string `json:"updated_at,omitempty"`
}

type projectVolumeOut struct {
	ID                     string                       `json:"id"`
	ProjectID              string                       `json:"project_id"`
	ServerID               *string                      `json:"server_id,omitempty"`
	AttachedVMID           *string                      `json:"attached_vm_id,omitempty"`
	MountPath              string                       `json:"mount_path"`
	SizeGB                 int32                        `json:"size_gb"`
	Filesystem             string                       `json:"filesystem"`
	Status                 string                       `json:"status"`
	Health                 string                       `json:"health"`
	BackupPolicy           projectVolumeBackupPolicyOut `json:"backup_policy"`
	CurrentOperation       *projectVolumeOperationOut   `json:"current_operation,omitempty"`
	LastSuccessfulBackupAt *string                      `json:"last_successful_backup_at,omitempty"`
	LastError              string                       `json:"last_error,omitempty"`
	CreatedAt              *string                      `json:"created_at,omitempty"`
	UpdatedAt              *string                      `json:"updated_at,omitempty"`
}

func projectVolumeToOut(v queries.ProjectVolume) projectVolumeOut {
	return projectVolumeOut{
		ID:           pguuid.ToString(v.ID),
		ProjectID:    pguuid.ToString(v.ProjectID),
		ServerID:     optionalUUIDString(v.ServerID),
		AttachedVMID: optionalUUIDString(v.AttachedVmID),
		MountPath:    v.MountPath,
		SizeGB:       v.SizeGb,
		Filesystem:   v.Filesystem,
		Status:       v.Status,
		Health:       v.Health,
		BackupPolicy: projectVolumeBackupPolicyOut{
			Schedule:               v.BackupSchedule,
			RetentionCount:         v.BackupRetentionCount,
			PreDeleteBackupEnabled: v.PreDeleteBackupEnabled,
		},
		LastError: strings.TrimSpace(v.LastError),
		CreatedAt: formatTS(v.CreatedAt),
		UpdatedAt: formatTS(v.UpdatedAt),
	}
}

func projectVolumeToOutCtx(ctx context.Context, q *queries.Queries, v queries.ProjectVolume) projectVolumeOut {
	out := projectVolumeToOut(v)
	if q == nil {
		return out
	}
	if op, err := q.ProjectVolumeOperationFindCurrentByVolumeID(ctx, v.ID); err == nil {
		opOut := projectVolumeOperationToOut(op)
		out.CurrentOperation = &opOut
	}
	if backup, err := q.ProjectVolumeBackupFindLastSuccessfulByProjectID(ctx, v.ProjectID); err == nil {
		out.LastSuccessfulBackupAt = formatTS(backup.CompletedAt)
	}
	return out
}

func projectVolumeOperationToOut(op queries.ProjectVolumeOperation) projectVolumeOperationOut {
	var meta struct {
		BackupID       string `json:"backup_id"`
		TargetServerID string `json:"target_server_id"`
	}
	_ = json.Unmarshal(op.RequestMetadata, &meta)
	var targetServerID *string
	if s := strings.TrimSpace(meta.TargetServerID); s != "" {
		targetServerID = &s
	}
	var backupID *string
	if s := strings.TrimSpace(meta.BackupID); s != "" {
		backupID = &s
	}
	return projectVolumeOperationOut{
		ID:             pguuid.ToString(op.ID),
		Kind:           op.Kind,
		Status:         op.Status,
		ServerID:       optionalUUIDString(op.ServerID),
		TargetServerID: targetServerID,
		BackupID:       backupID,
		Error:          strings.TrimSpace(op.Error),
		StartedAt:      formatTS(op.StartedAt),
		CompletedAt:    formatTS(op.CompletedAt),
		FailedAt:       formatTS(op.FailedAt),
		CreatedAt:      formatTS(op.CreatedAt),
		UpdatedAt:      formatTS(op.UpdatedAt),
	}
}

func projectVolumeBackupToOut(backup queries.ProjectVolumeBackup) projectVolumeBackupOut {
	return projectVolumeBackupOut{
		ID:          pguuid.ToString(backup.ID),
		Kind:        backup.Kind,
		Status:      backup.Status,
		StorageURL:  strings.TrimSpace(backup.StorageUrl),
		SizeBytes:   backup.SizeBytes,
		Error:       strings.TrimSpace(backup.Error),
		StartedAt:   formatTS(backup.StartedAt),
		CompletedAt: formatTS(backup.CompletedAt),
		FailedAt:    formatTS(backup.FailedAt),
		CreatedAt:   formatTS(backup.CreatedAt),
		UpdatedAt:   formatTS(backup.UpdatedAt),
	}
}

func normalizeProjectVolumeMountPath(raw string) (string, error) {
	mountPath := strings.TrimSpace(raw)
	if mountPath == "" {
		mountPath = "/data"
	}
	if !strings.HasPrefix(mountPath, "/") {
		return "", errors.New("mount_path must be absolute")
	}
	clean := path.Clean(mountPath)
	if clean == "/" {
		return "", errors.New("mount_path cannot be /")
	}
	return clean, nil
}

func normalizeProjectVolumeBackupSchedule(raw string) (string, error) {
	schedule := strings.TrimSpace(raw)
	if schedule == "" {
		schedule = "manual"
	}
	switch schedule {
	case "off", "manual", "daily", "weekly":
		return schedule, nil
	default:
		return "", fmt.Errorf("backup_schedule must be one of off, manual, daily, weekly")
	}
}

func normalizeProjectVolumeBackupRetention(raw *int32) (int32, error) {
	retention := int32(7)
	if raw != nil {
		retention = *raw
	}
	if retention <= 0 {
		return 0, fmt.Errorf("backup_retention_count must be at least 1")
	}
	return retention, nil
}

func hasCloudHypervisorWorker(rows []queries.ServerComponentStatus, servers []queries.Server) bool {
	serverStatus := make(map[[16]byte]string, len(servers))
	for _, server := range servers {
		if !server.ID.Valid {
			continue
		}
		serverStatus[server.ID.Bytes] = server.Status
	}
	for _, row := range rows {
		if row.Component != "worker" || row.Status != "healthy" || !row.ServerID.Valid {
			continue
		}
		if serverStatus[row.ServerID.Bytes] != "active" {
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

func (a *API) getProjectVolume(w http.ResponseWriter, r *http.Request) {
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
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "get_project_volume", err)
		return
	}
	writeJSON(w, http.StatusOK, projectVolumeToOutCtx(r.Context(), a.q, vol))
}

func (a *API) putProjectVolume(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	projectID, project, ok := a.projectForRequest(w, r, p.OrganizationID)
	if !ok {
		return
	}

	var req struct {
		MountPath              *string `json:"mount_path"`
		SizeGB                 *int32  `json:"size_gb"`
		BackupSchedule         *string `json:"backup_schedule"`
		BackupRetentionCount   *int32  `json:"backup_retention_count"`
		PreDeleteBackupEnabled *bool   `json:"pre_delete_backup_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	sizeGB := int32(10)
	if req.SizeGB != nil {
		sizeGB = *req.SizeGB
	}
	if sizeGB <= 0 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "size_gb must be at least 1")
		return
	}
	mountPath, err := normalizeProjectVolumeMountPath(pgTextStringPtr(req.MountPath))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	backupSchedule, err := normalizeProjectVolumeBackupSchedule(pgTextStringPtr(req.BackupSchedule))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	backupRetentionCount, err := normalizeProjectVolumeBackupRetention(req.BackupRetentionCount)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	preDeleteBackupEnabled := req.PreDeleteBackupEnabled != nil && *req.PreDeleteBackupEnabled
	if project.MaxInstanceCount > 1 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "persistent volumes require max_instance_count <= 1")
		return
	}
	servers, err := a.q.ServerFindAll(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
		return
	}
	statuses, err := a.q.ServerComponentStatusFindAll(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
		return
	}
	if !hasCloudHypervisorWorker(statuses, servers) {
		writeAPIError(w, http.StatusConflict, "volume_unavailable", "no active cloud-hypervisor worker is available")
		return
	}

	vol, err := a.q.ProjectVolumeFindByProjectID(r.Context(), projectID)
	switch {
	case err == nil:
		if req.BackupSchedule == nil {
			backupSchedule = vol.BackupSchedule
		}
		if req.BackupRetentionCount == nil {
			backupRetentionCount = vol.BackupRetentionCount
		}
		if req.PreDeleteBackupEnabled == nil {
			preDeleteBackupEnabled = vol.PreDeleteBackupEnabled
		}
		if vol.MountPath != mountPath {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "mount_path is immutable after creation")
			return
		}
		if sizeGB < vol.SizeGb {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "size_gb cannot shrink an existing volume")
			return
		}
		if vol.Status == "attached" && sizeGB != vol.SizeGb {
			writeAPIError(w, http.StatusConflict, "volume_attached", "detach the volume before resizing it")
			return
		}
		vol, err = a.q.ProjectVolumeUpdateSpec(r.Context(), queries.ProjectVolumeUpdateSpecParams{
			ProjectID:              projectID,
			SizeGb:                 sizeGB,
			Filesystem:             "ext4",
			BackupSchedule:         backupSchedule,
			BackupRetentionCount:   backupRetentionCount,
			PreDeleteBackupEnabled: preDeleteBackupEnabled,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
			return
		}
	case errors.Is(err, pgx.ErrNoRows):
		anyVol, anyErr := a.q.ProjectVolumeFindAnyByProjectID(r.Context(), projectID)
		switch {
		case anyErr == nil:
			if req.BackupSchedule == nil {
				backupSchedule = anyVol.BackupSchedule
			}
			if req.BackupRetentionCount == nil {
				backupRetentionCount = anyVol.BackupRetentionCount
			}
			if req.PreDeleteBackupEnabled == nil {
				preDeleteBackupEnabled = anyVol.PreDeleteBackupEnabled
			}
			if anyVol.MountPath != mountPath {
				writeAPIError(w, http.StatusBadRequest, "validation_error", "mount_path is immutable after creation")
				return
			}
			if sizeGB < anyVol.SizeGb {
				writeAPIError(w, http.StatusBadRequest, "validation_error", "size_gb cannot shrink an existing volume")
				return
			}
			vol, err = a.q.ProjectVolumeRevive(r.Context(), queries.ProjectVolumeReviveParams{
				ProjectID:              projectID,
				SizeGb:                 sizeGB,
				Filesystem:             "ext4",
				BackupSchedule:         backupSchedule,
				BackupRetentionCount:   backupRetentionCount,
				PreDeleteBackupEnabled: preDeleteBackupEnabled,
			})
			if err != nil {
				writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
				return
			}
		case errors.Is(anyErr, pgx.ErrNoRows):
			vol, err = a.q.ProjectVolumeCreate(r.Context(), queries.ProjectVolumeCreateParams{
				ID:                     pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:              projectID,
				MountPath:              mountPath,
				SizeGb:                 sizeGB,
				Filesystem:             "ext4",
				BackupSchedule:         backupSchedule,
				BackupRetentionCount:   backupRetentionCount,
				PreDeleteBackupEnabled: preDeleteBackupEnabled,
			})
			if err != nil {
				writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
				return
			}
		default:
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", anyErr)
			return
		}
	default:
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
		return
	}

	if a.dashboardEvents != nil {
		a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	}
	writeJSON(w, http.StatusOK, projectVolumeToOutCtx(r.Context(), a.q, vol))
}

func (a *API) deleteProjectVolume(w http.ResponseWriter, r *http.Request) {
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
	vol, err := a.q.ProjectVolumeFindByProjectID(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "project volume not found")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if vol.Status == "attached" || vol.AttachedVmID.Valid {
		writeAPIError(w, http.StatusConflict, "volume_attached", "detach the volume before deleting it")
		return
	}
	if isProjectVolumeTransitionalStatus(vol.Status) {
		writeAPIError(w, http.StatusConflict, "volume_busy", "another volume operation is already in progress")
		return
	}
	if _, err := a.q.ProjectVolumeOperationFindCurrentByVolumeID(r.Context(), vol.ID); err == nil {
		writeAPIError(w, http.StatusConflict, "volume_busy", "another volume operation is already in progress")
		return
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if vol.PreDeleteBackupEnabled {
		if !vol.ServerID.Valid {
			writeAPIError(w, http.StatusConflict, "backup_store_unavailable", "volume must be pinned to a worker before a pre-delete backup can run")
			return
		}
		if err := a.ensureVolumeBackupStoreConfigured(); err != nil {
			writeAPIError(w, http.StatusConflict, "backup_store_unavailable", err.Error())
			return
		}
		backup, err := a.q.ProjectVolumeBackupCreate(r.Context(), queries.ProjectVolumeBackupCreateParams{
			ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
			ProjectVolumeID: vol.ID,
			Kind:            "pre_delete",
			Metadata:        []byte(`{}`),
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
			return
		}
		op, err := a.enqueueProjectVolumeOperation(r.Context(), vol, vol.ServerID, "backup", projectVolumeOperationRequest{
			BackupID:    pguuid.ToString(backup.ID),
			BackupKind:  "pre_delete",
			DeleteAfter: true,
		}, "deleting")
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
			return
		}
		a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
		writeJSON(w, http.StatusAccepted, projectVolumeOperationToOut(op))
		return
	}
	if err := os.Remove(runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))); err != nil && !os.IsNotExist(err) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if err := a.q.ProjectVolumeSoftDelete(r.Context(), projectID); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if a.dashboardEvents != nil {
		a.publishProjectVolumeTopics(uuid.UUID(projectID.Bytes))
	}
	w.WriteHeader(http.StatusNoContent)
}

func pgTextStringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
