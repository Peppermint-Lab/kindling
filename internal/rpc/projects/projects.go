// Package projects provides project management API handlers.
package projects

import (
	"net/http"
)

// Handlers holds the handler functions for project routes.
type Handlers struct {
	ListProjects       http.HandlerFunc
	CreateProject      http.HandlerFunc
	GetProject         http.HandlerFunc
	PatchProject       http.HandlerFunc
	DeleteProject      http.HandlerFunc
	ListProjectSecrets http.HandlerFunc
	UpsertProjectSecret http.HandlerFunc
	DeleteProjectSecret http.HandlerFunc
	ListProjectPreviews       http.HandlerFunc
	RedeployProjectPreview    http.HandlerFunc
	DeleteProjectPreview      http.HandlerFunc
	GetGitHubSetup            http.HandlerFunc
	GitHead                   http.HandlerFunc
	RotateWebhookSecret       http.HandlerFunc
	GetMeta                   http.HandlerFunc
	PutMeta                   http.HandlerFunc
	GetProjectUsageCurrent    http.HandlerFunc
	GetProjectUsageHistory    http.HandlerFunc
}

// RegisterRoutes mounts project management routes on the given mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/meta", h.GetMeta)
	mux.HandleFunc("PUT /api/meta", h.PutMeta)
	mux.HandleFunc("GET /api/projects", h.ListProjects)
	mux.HandleFunc("POST /api/projects", h.CreateProject)
	mux.HandleFunc("GET /api/projects/{id}", h.GetProject)
	mux.HandleFunc("PATCH /api/projects/{id}", h.PatchProject)
	mux.HandleFunc("DELETE /api/projects/{id}", h.DeleteProject)
	mux.HandleFunc("GET /api/projects/{id}/secrets", h.ListProjectSecrets)
	mux.HandleFunc("POST /api/projects/{id}/secrets", h.UpsertProjectSecret)
	mux.HandleFunc("DELETE /api/projects/{id}/secrets/{secret_id}", h.DeleteProjectSecret)
	mux.HandleFunc("GET /api/projects/{id}/previews", h.ListProjectPreviews)
	mux.HandleFunc("POST /api/projects/{id}/previews/{preview_id}/redeploy", h.RedeployProjectPreview)
	mux.HandleFunc("DELETE /api/projects/{id}/previews/{preview_id}", h.DeleteProjectPreview)
	mux.HandleFunc("GET /api/projects/{id}/github-setup", h.GetGitHubSetup)
	mux.HandleFunc("GET /api/projects/{id}/git-head", h.GitHead)
	mux.HandleFunc("POST /api/projects/{id}/rotate-webhook-secret", h.RotateWebhookSecret)
	mux.HandleFunc("GET /api/projects/{id}/usage/current", h.GetProjectUsageCurrent)
	mux.HandleFunc("GET /api/projects/{id}/usage/history", h.GetProjectUsageHistory)
}
