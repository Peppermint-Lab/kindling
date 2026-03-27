package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func projectStripSecret(p queries.Project) queries.Project {
	p.GithubWebhookSecret = ""
	return p
}

func validatePersistentVolumeReplicaCount(desired int32, volumeExists bool) error {
	if volumeExists && desired > 1 {
		return errors.New("persistent volumes require desired_instance_count <= 1")
	}
	return nil
}

func (a *API) ensurePersistentVolumeReplicaCount(ctx context.Context, projectID pgtype.UUID, desired int32) error {
	if desired <= 1 {
		return nil
	}
	_, err := a.q.ProjectVolumeFindByProjectID(ctx, projectID)
	switch {
	case err == nil:
		return validatePersistentVolumeReplicaCount(desired, true)
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	default:
		return fmt.Errorf("find project volume: %w", err)
	}
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projects, err := a.q.ProjectFindAllByOrgID(r.Context(), p.OrganizationID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_projects", err)
		return
	}
	out := make([]queries.Project, len(projects))
	for i := range projects {
		out[i] = projectStripSecret(projects[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	var req struct {
		Name                   string `json:"name"`
		GithubRepository       string `json:"github_repository"`
		DockerfilePath         string `json:"dockerfile_path"`
		RootDirectory          string `json:"root_directory"`
		DesiredInstanceCount   *int32 `json:"desired_instance_count"`
		ScaleToZeroEnabled     *bool  `json:"scale_to_zero_enabled"`
		BuildOnlyOnRootChanges *bool  `json:"build_only_on_root_changes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "https://github.com/")
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "http://github.com/")
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "github.com/")
	req.GithubRepository = strings.TrimSuffix(req.GithubRepository, ".git")

	if req.DockerfilePath == "" {
		req.DockerfilePath = "Dockerfile"
	}
	if strings.TrimSpace(req.RootDirectory) == "" {
		req.RootDirectory = "/"
	}

	var webhookSecret string
	if req.GithubRepository != "" {
		s, err := GenerateWebhookSecret()
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "secret_generation", err)
			return
		}
		webhookSecret = s
	}

	desired := int32(1)
	if req.DesiredInstanceCount != nil {
		desired = *req.DesiredInstanceCount
	}
	if desired < 0 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "desired_instance_count must be at least 0")
		return
	}
	params := queries.ProjectCreateParams{
		ID:                     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrgID:                  p.OrganizationID,
		Name:                   req.Name,
		GithubRepository:       req.GithubRepository,
		GithubInstallationID:   0,
		GithubWebhookSecret:    webhookSecret,
		RootDirectory:          req.RootDirectory,
		DockerfilePath:         req.DockerfilePath,
		DesiredInstanceCount:   desired,
		BuildOnlyOnRootChanges: req.BuildOnlyOnRootChanges != nil && *req.BuildOnlyOnRootChanges,
	}
	project, err := a.q.ProjectCreate(r.Context(), params)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project", err)
		return
	}
	if req.ScaleToZeroEnabled != nil && *req.ScaleToZeroEnabled {
		project, err = a.q.ProjectUpdateScaleToZeroEnabled(r.Context(), queries.ProjectUpdateScaleToZeroEnabledParams{
			ID:                 project.ID,
			ScaleToZeroEnabled: true,
			OrgID:              p.OrganizationID,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_project", err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, projectStripSecret(project))
}

func (a *API) patchProject(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	var req struct {
		DesiredInstanceCount   *int32 `json:"desired_instance_count"`
		ScaleToZeroEnabled     *bool  `json:"scale_to_zero_enabled"`
		BuildOnlyOnRootChanges *bool  `json:"build_only_on_root_changes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.DesiredInstanceCount == nil && req.ScaleToZeroEnabled == nil && req.BuildOnlyOnRootChanges == nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "provide desired_instance_count and/or scale_to_zero_enabled and/or build_only_on_root_changes")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var project queries.Project
	if req.DesiredInstanceCount != nil {
		if *req.DesiredInstanceCount < 0 {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "desired_instance_count must be at least 0")
			return
		}
		if err := a.ensurePersistentVolumeReplicaCount(r.Context(), id, *req.DesiredInstanceCount); err != nil {
			if err.Error() == "persistent volumes require desired_instance_count <= 1" {
				writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
				return
			}
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
			return
		}
		project, err = a.q.ProjectUpdateDesiredInstanceCount(r.Context(), queries.ProjectUpdateDesiredInstanceCountParams{
			ID:                   id,
			DesiredInstanceCount: *req.DesiredInstanceCount,
			OrgID:                p.OrganizationID,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
			return
		}
		if *req.DesiredInstanceCount > 0 {
			_ = a.q.ProjectClearScaledToZero(r.Context(), id)
		}
	} else {
		project, err = a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
			ID:    id,
			OrgID: p.OrganizationID,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
			return
		}
	}
	if req.ScaleToZeroEnabled != nil {
		project, err = a.q.ProjectUpdateScaleToZeroEnabled(r.Context(), queries.ProjectUpdateScaleToZeroEnabledParams{
			ID:                 id,
			ScaleToZeroEnabled: *req.ScaleToZeroEnabled,
			OrgID:              p.OrganizationID,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
			return
		}
		if !*req.ScaleToZeroEnabled {
			_ = a.q.ProjectClearScaledToZero(r.Context(), id)
		}
	}
	if req.BuildOnlyOnRootChanges != nil {
		project, err = a.q.ProjectUpdateBuildOnlyOnRootChanges(r.Context(), queries.ProjectUpdateBuildOnlyOnRootChangesParams{
			ID:                     id,
			BuildOnlyOnRootChanges: *req.BuildOnlyOnRootChanges,
			OrgID:                  p.OrganizationID,
		})
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
			return
		}
	}
	project, err = a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_project", err)
		return
	}
	writeJSON(w, http.StatusOK, projectStripSecret(project))
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	writeJSON(w, http.StatusOK, projectStripSecret(project))
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if err := a.q.ProjectDeleteByIDAndOrg(r.Context(), queries.ProjectDeleteByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_project", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getGitHubSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	base, err := a.publicBaseURL(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	whURL := ""
	if base != "" {
		whURL = base + "/webhooks/github"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"github_repository":          project.GithubRepository,
		"webhook_url":                whURL,
		"webhook_path":               "/webhooks/github",
		"webhook_secret":             project.GithubWebhookSecret,
		"public_base_url_configured": base != "",
		"instructions":               "In GitHub: Settings → Webhooks → Add webhook. Use the payload URL and secret below. Content type: application/json. Events: push (production) and pull_request (PR previews; requires cluster preview_base_domain).",
	})
}

func (a *API) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	secret, err := GenerateWebhookSecret()
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "secret_generation", err)
		return
	}
	project, err := a.q.ProjectUpdateWebhookSecret(r.Context(), queries.ProjectUpdateWebhookSecretParams{
		ID:                  id,
		GithubWebhookSecret: secret,
		OrgID:               p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "update_secret", err)
		return
	}
	base, err := a.publicBaseURL(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	whURL := ""
	if base != "" {
		whURL = base + "/webhooks/github"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"github_webhook_secret": project.GithubWebhookSecret,
		"webhook_url":           whURL,
	})
}
