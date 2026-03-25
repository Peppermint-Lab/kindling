package rpc

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

// API provides REST endpoints for the dashboard.
type API struct {
	q *queries.Queries
}

// NewAPI creates a new API handler.
func NewAPI(q *queries.Queries) *API {
	return &API{q: q}
}

// Register mounts all API routes on the given mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", a.listProjects)
	mux.HandleFunc("POST /api/projects", a.createProject)
	mux.HandleFunc("GET /api/projects/{id}", a.getProject)
	mux.HandleFunc("DELETE /api/projects/{id}", a.deleteProject)
	mux.HandleFunc("GET /api/projects/{id}/deployments", a.listDeployments)
	mux.HandleFunc("GET /api/deployments/{id}", a.getDeployment)
	mux.HandleFunc("GET /api/deployments/{id}/logs", a.getDeploymentLogs)
	mux.HandleFunc("POST /api/projects/{id}/deploy", a.triggerDeploy)
	mux.HandleFunc("GET /api/servers", a.listServers)
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := a.q.ProjectFindAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		GithubRepository string `json:"github_repository"`
		DockerfilePath   string `json:"dockerfile_path"`
		RootDirectory    string `json:"root_directory"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}
	// Normalize GitHub repository: strip full URL to owner/repo.
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "https://github.com/")
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "http://github.com/")
	req.GithubRepository = strings.TrimPrefix(req.GithubRepository, "github.com/")
	req.GithubRepository = strings.TrimSuffix(req.GithubRepository, ".git")

	if req.DockerfilePath == "" {
		req.DockerfilePath = "Dockerfile"
	}

	project, err := a.q.ProjectCreate(r.Context(), queries.ProjectCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Name:                 req.Name,
		GithubRepository:     req.GithubRepository,
		GithubInstallationID: 0,
		GithubWebhookSecret:  "",
		RootDirectory:        req.RootDirectory,
		DockerfilePath:       req.DockerfilePath,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	project, err := a.q.ProjectFirstByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	if err := a.q.ProjectDelete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	deployments, err := a.q.DeploymentFindByProjectID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (a *API) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}
	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, dep)
}

func (a *API) getDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	if !dep.BuildID.Valid {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	logs, err := a.q.BuildLogsByBuildID(r.Context(), dep.BuildID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (a *API) triggerDeploy(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUID(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"invalid id"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Commit string `json:"commit"`
	}
	json.NewDecoder(r.Body).Decode(&req) // body is optional
	if req.Commit == "" {
		req.Commit = "main"
	}

	// Verify project exists.
	if _, err := a.q.ProjectFirstByID(r.Context(), projectID); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	dep, err := a.q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:    projectID,
		GithubCommit: req.Commit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, dep)
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.q.ServerFindAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, err error) {
	slog.Error("API error", "status", status, "error", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func parseUUID(s string) (pgtype.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
