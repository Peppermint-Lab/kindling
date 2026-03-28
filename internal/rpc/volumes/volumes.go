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
	GetServiceVolume         http.HandlerFunc
	PutServiceVolume         http.HandlerFunc
	DeleteServiceVolume      http.HandlerFunc
	ListServiceVolumeBackups http.HandlerFunc
	PostServiceVolumeBackup  http.HandlerFunc
	PostServiceVolumeRestore http.HandlerFunc
	PostServiceVolumeMove    http.HandlerFunc
	PostServiceVolumeRepair  http.HandlerFunc
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
	mux.HandleFunc("GET /api/services/{id}/volume", h.GetServiceVolume)
	mux.HandleFunc("PUT /api/services/{id}/volume", h.PutServiceVolume)
	mux.HandleFunc("DELETE /api/services/{id}/volume", h.DeleteServiceVolume)
	mux.HandleFunc("GET /api/services/{id}/volume/backups", h.ListServiceVolumeBackups)
	mux.HandleFunc("POST /api/services/{id}/volume/backups", h.PostServiceVolumeBackup)
	mux.HandleFunc("POST /api/services/{id}/volume/restore", h.PostServiceVolumeRestore)
	mux.HandleFunc("POST /api/services/{id}/volume/move", h.PostServiceVolumeMove)
	mux.HandleFunc("POST /api/services/{id}/volume/repair", h.PostServiceVolumeRepair)
}
