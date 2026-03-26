package rpc

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// API provides REST endpoints for the dashboard.
type API struct {
	q   *queries.Queries
	cfg *config.Manager
}

// NewAPI creates a new API handler. cfg supplies DB-backed secrets (e.g. GitHub token).
func NewAPI(q *queries.Queries, cfg *config.Manager) *API {
	return &API{q: q, cfg: cfg}
}

func (a *API) gitHubToken() string {
	if a.cfg == nil {
		return ""
	}
	s := a.cfg.Snapshot()
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.GitHubToken)
}

// Register mounts all API routes on the given mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/meta", a.getMeta)
	mux.HandleFunc("PUT /api/meta", a.putMeta)
	mux.HandleFunc("GET /api/projects", a.listProjects)
	mux.HandleFunc("POST /api/projects", a.createProject)
	mux.HandleFunc("GET /api/projects/{id}", a.getProject)
	mux.HandleFunc("PATCH /api/projects/{id}", a.patchProject)
	mux.HandleFunc("DELETE /api/projects/{id}", a.deleteProject)
	mux.HandleFunc("GET /api/projects/{id}/deployments", a.listDeployments)
	mux.HandleFunc("GET /api/projects/{id}/github-setup", a.getGitHubSetup)
	mux.HandleFunc("GET /api/projects/{id}/git-head", a.gitHead)
	mux.HandleFunc("POST /api/projects/{id}/rotate-webhook-secret", a.rotateWebhookSecret)
	mux.HandleFunc("GET /api/projects/{id}/domains", a.listProjectDomains)
	mux.HandleFunc("POST /api/projects/{id}/domains", a.createProjectDomain)
	mux.HandleFunc("DELETE /api/projects/{id}/domains/{domain_id}", a.deleteProjectDomain)
	mux.HandleFunc("POST /api/projects/{id}/domains/{domain_id}/verify", a.verifyProjectDomain)
	mux.HandleFunc("GET /api/deployments", a.listAllDeployments)
	mux.HandleFunc("GET /api/deployments/{id}", a.getDeployment)
	mux.HandleFunc("GET /api/deployments/{id}/logs", a.getDeploymentLogs)
	mux.HandleFunc("GET /api/deployments/{id}/stream", a.streamDeployment)
	mux.HandleFunc("POST /api/projects/{id}/deploy", a.triggerDeploy)
	mux.HandleFunc("POST /api/deployments/{id}/cancel", a.cancelDeployment)
	mux.HandleFunc("GET /api/servers", a.listServers)
	a.registerUsageRoutes(mux)
	a.registerAuthRoutes(mux)
	a.registerProviderRoutes(mux)
}

func (a *API) getMeta(w http.ResponseWriter, r *http.Request) {
	if _, ok := mustPrincipal(w, r); !ok {
		return
	}
	base, err := a.publicBaseURL(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	dash, err := a.dashboardPublicHost(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	out := map[string]any{
		"public_base_url":            base,
		"public_base_url_configured": base != "",
		"webhook_path":               "/webhooks/github",
	}
	if dash != "" {
		out["dashboard_public_host"] = dash
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) putMeta(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if p.OrgRole != "owner" && p.OrgRole != "admin" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
		return
	}
	var req struct {
		PublicBaseURL       *string `json:"public_base_url"`
		DashboardPublicHost *string `json:"dashboard_public_host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.PublicBaseURL != nil {
		if err := a.clusterSettingUpsertPublicBaseURL(r.Context(), *req.PublicBaseURL); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	if req.DashboardPublicHost != nil {
		if err := a.clusterSettingUpsertDashboardPublicHost(r.Context(), *req.DashboardPublicHost); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
			return
		}
	}
	base, err := a.publicBaseURL(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	dash, err := a.dashboardPublicHost(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cluster_settings", err)
		return
	}
	out := map[string]any{
		"public_base_url":            base,
		"public_base_url_configured": base != "",
		"webhook_path":               "/webhooks/github",
	}
	if dash != "" {
		out["dashboard_public_host"] = dash
	}
	writeJSON(w, http.StatusOK, out)
}

func projectStripSecret(p queries.Project) queries.Project {
	p.GithubWebhookSecret = ""
	return p
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
	var req struct {
		Name                 string `json:"name"`
		GithubRepository     string `json:"github_repository"`
		DockerfilePath       string `json:"dockerfile_path"`
		RootDirectory        string `json:"root_directory"`
		DesiredInstanceCount *int32 `json:"desired_instance_count"`
		ScaleToZeroEnabled   *bool  `json:"scale_to_zero_enabled"`
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
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrgID:                p.OrganizationID,
		Name:                 req.Name,
		GithubRepository:     req.GithubRepository,
		GithubInstallationID: 0,
		GithubWebhookSecret:  webhookSecret,
		RootDirectory:        req.RootDirectory,
		DockerfilePath:       req.DockerfilePath,
		DesiredInstanceCount: desired,
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
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	var req struct {
		DesiredInstanceCount *int32 `json:"desired_instance_count"`
		ScaleToZeroEnabled   *bool  `json:"scale_to_zero_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.DesiredInstanceCount == nil && req.ScaleToZeroEnabled == nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "provide desired_instance_count and/or scale_to_zero_enabled")
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
		"instructions":               "In GitHub: Settings → Webhooks → Add webhook. Use the payload URL and secret below. Content type: application/json. Events: Just the push event.",
	})
}

func (a *API) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
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

func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
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
	deployments, err := a.q.DeploymentFindByProjectID(r.Context(), id)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_deployments", err)
		return
	}
	out := make([]deploymentOut, 0, len(deployments))
	for _, d := range deployments {
		out = append(out, a.deploymentToOutCtx(r.Context(), d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) listAllDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	limit := int32(50)
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	rows, err := a.q.DeploymentFindRecentWithProjectForOrg(r.Context(), queries.DeploymentFindRecentWithProjectForOrgParams{
		Limit: limit,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_deployments", err)
		return
	}
	out := make([]deploymentListItemOut, len(rows))
	for i := range rows {
		out[i] = a.listRowForOrgToOutCtx(r.Context(), rows[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}
	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	writeJSON(w, http.StatusOK, a.deploymentToOutCtx(r.Context(), dep))
}

func (a *API) getDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}

	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	if !dep.BuildID.Valid {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	logs, err := a.q.BuildLogsByBuildID(r.Context(), dep.BuildID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "build_logs", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (a *API) triggerDeploy(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}

	var req struct {
		Commit string `json:"commit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Commit == "" {
		req.Commit = "main"
	}

	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	dep, err := a.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:    projectID,
		GithubCommit: req.Commit,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_deployment", err)
		return
	}

	writeJSON(w, http.StatusCreated, a.deploymentToOutCtx(r.Context(), dep))
}

func (a *API) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}
	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	if err := a.q.DeploymentUpdateFailedAt(r.Context(), id); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "cancel_deployment", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := mustPrincipal(w, r); !ok {
		return
	}
	servers, err := a.q.ServerFindAll(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_servers", err)
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func parseUUID(s string) (pgtype.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
