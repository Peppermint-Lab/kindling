// Package volumes provides project volume management API handlers.
package volumes

import (
	"net/http"
)

// Handlers holds the handler functions for volume routes.
type Handlers struct {
	GetProjectVolume         http.HandlerFunc
	PutProjectVolume         http.HandlerFunc
	DeleteProjectVolume      http.HandlerFunc
	ListProjectVolumeBackups http.HandlerFunc
	PostProjectVolumeBackup  http.HandlerFunc
	PostProjectVolumeRestore http.HandlerFunc
	PostProjectVolumeMove    http.HandlerFunc
	PostProjectVolumeRepair  http.HandlerFunc
}

// RegisterRoutes mounts volume routes on the given mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/volume", h.GetProjectVolume)
	mux.HandleFunc("PUT /api/projects/{id}/volume", h.PutProjectVolume)
	mux.HandleFunc("DELETE /api/projects/{id}/volume", h.DeleteProjectVolume)
	mux.HandleFunc("GET /api/projects/{id}/volume/backups", h.ListProjectVolumeBackups)
	mux.HandleFunc("POST /api/projects/{id}/volume/backups", h.PostProjectVolumeBackup)
	mux.HandleFunc("POST /api/projects/{id}/volume/restore", h.PostProjectVolumeRestore)
	mux.HandleFunc("POST /api/projects/{id}/volume/move", h.PostProjectVolumeMove)
	mux.HandleFunc("POST /api/projects/{id}/volume/repair", h.PostProjectVolumeRepair)
}
