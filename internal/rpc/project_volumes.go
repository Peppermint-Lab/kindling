package rpc

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/runtime"
)

type projectVolumeOut struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	ServerID     *string `json:"server_id,omitempty"`
	AttachedVMID *string `json:"attached_vm_id,omitempty"`
	MountPath    string  `json:"mount_path"`
	SizeGB       int32   `json:"size_gb"`
	Filesystem   string  `json:"filesystem"`
	Status       string  `json:"status"`
	LastError    string  `json:"last_error,omitempty"`
	CreatedAt    *string `json:"created_at,omitempty"`
	UpdatedAt    *string `json:"updated_at,omitempty"`
}

func projectVolumeToOut(v queries.ProjectVolume) projectVolumeOut {
	return projectVolumeOut{
		ID:           pgUUIDToString(v.ID),
		ProjectID:    pgUUIDToString(v.ProjectID),
		ServerID:     optionalUUIDString(v.ServerID),
		AttachedVMID: optionalUUIDString(v.AttachedVmID),
		MountPath:    v.MountPath,
		SizeGB:       v.SizeGb,
		Filesystem:   v.Filesystem,
		Status:       v.Status,
		LastError:    strings.TrimSpace(v.LastError),
		CreatedAt:    formatTS(v.CreatedAt),
		UpdatedAt:    formatTS(v.UpdatedAt),
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
	writeJSON(w, http.StatusOK, projectVolumeToOut(vol))
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
		MountPath *string `json:"mount_path"`
		SizeGB    *int32  `json:"size_gb"`
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
	if project.DesiredInstanceCount > 1 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "persistent volumes require desired_instance_count <= 1")
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
			ProjectID:  projectID,
			SizeGb:     sizeGB,
			Filesystem: "ext4",
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
			return
		}
	case errors.Is(err, pgx.ErrNoRows):
		anyVol, anyErr := a.q.ProjectVolumeFindAnyByProjectID(r.Context(), projectID)
		switch {
		case anyErr == nil:
			if anyVol.MountPath != mountPath {
				writeAPIError(w, http.StatusBadRequest, "validation_error", "mount_path is immutable after creation")
				return
			}
			if sizeGB < anyVol.SizeGb {
				writeAPIError(w, http.StatusBadRequest, "validation_error", "size_gb cannot shrink an existing volume")
				return
			}
			vol, err = a.q.ProjectVolumeRevive(r.Context(), queries.ProjectVolumeReviveParams{
				ProjectID:  projectID,
				SizeGb:     sizeGB,
				Filesystem: "ext4",
			})
			if err != nil {
				writeAPIErrorFromErr(w, http.StatusInternalServerError, "put_project_volume", err)
				return
			}
		case errors.Is(anyErr, pgx.ErrNoRows):
			vol, err = a.q.ProjectVolumeCreate(r.Context(), queries.ProjectVolumeCreateParams{
				ID:         pgtype.UUID{Bytes: uuid.New(), Valid: true},
				ProjectID:  projectID,
				MountPath:  mountPath,
				SizeGb:     sizeGB,
				Filesystem: "ext4",
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
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(projectID.Bytes)))
	}
	writeJSON(w, http.StatusOK, projectVolumeToOut(vol))
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
	if err := os.Remove(runtime.PersistentVolumePath(uuid.UUID(vol.ID.Bytes))); err != nil && !os.IsNotExist(err) {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if err := a.q.ProjectVolumeSoftDelete(r.Context(), projectID); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project_volume", err)
		return
	}
	if a.dashboardEvents != nil {
		a.dashboardEvents.Publish(TopicProject(uuid.UUID(projectID.Bytes)))
	}
	w.WriteHeader(http.StatusNoContent)
}

func pgTextStringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
