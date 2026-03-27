// Package auth provides authentication and authorization API handlers.
package auth

import (
	"net/http"
)

// Handlers holds the handler functions for auth routes.
type Handlers struct {
	AuthBootstrapStatus      http.HandlerFunc
	AuthSession              http.HandlerFunc
	AuthBootstrap            http.HandlerFunc
	AuthLogin                http.HandlerFunc
	AuthLogout               http.HandlerFunc
	AuthSwitchOrg            http.HandlerFunc
	ListAPIKeys              http.HandlerFunc
	CreateAPIKey             http.HandlerFunc
	RevokeAPIKey             http.HandlerFunc
	ListOrgProviders         http.HandlerFunc
	CreateOrgProvider        http.HandlerFunc
	DeleteOrgProvider        http.HandlerFunc
	ListPublicAuthProviders  http.HandlerFunc
	ListAdminAuthProviders   http.HandlerFunc
	PutAdminAuthProvider     http.HandlerFunc
	ListAuthIdentities       http.HandlerFunc
	StartExternalAuth        http.HandlerFunc
	LinkExternalAuth         http.HandlerFunc
	ExternalAuthCallback     http.HandlerFunc
	StreamDashboardEvents    http.HandlerFunc
}

// RegisterRoutes mounts authentication routes on the given mux.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	// Core auth
	mux.HandleFunc("GET /api/auth/bootstrap-status", h.AuthBootstrapStatus)
	mux.HandleFunc("GET /api/auth/session", h.AuthSession)
	mux.HandleFunc("POST /api/auth/bootstrap", h.AuthBootstrap)
	mux.HandleFunc("POST /api/auth/login", h.AuthLogin)
	mux.HandleFunc("POST /api/auth/logout", h.AuthLogout)
	mux.HandleFunc("POST /api/auth/switch-org", h.AuthSwitchOrg)
	// API keys
	mux.HandleFunc("GET /api/auth/api-keys", h.ListAPIKeys)
	mux.HandleFunc("POST /api/auth/api-keys", h.CreateAPIKey)
	mux.HandleFunc("DELETE /api/auth/api-keys/{id}", h.RevokeAPIKey)
	// External auth providers
	mux.HandleFunc("GET /api/auth/providers", h.ListPublicAuthProviders)
	mux.HandleFunc("GET /api/auth/admin/providers", h.ListAdminAuthProviders)
	mux.HandleFunc("PUT /api/auth/admin/providers/{provider}", h.PutAdminAuthProvider)
	mux.HandleFunc("GET /api/auth/identities", h.ListAuthIdentities)
	mux.HandleFunc("GET /api/auth/providers/{provider}/start", h.StartExternalAuth)
	mux.HandleFunc("GET /api/auth/providers/{provider}/link", h.LinkExternalAuth)
	mux.HandleFunc("GET /api/auth/providers/{provider}/callback", h.ExternalAuthCallback)
	// Org provider connections
	mux.HandleFunc("GET /api/org/provider-connections", h.ListOrgProviders)
	mux.HandleFunc("POST /api/org/provider-connections", h.CreateOrgProvider)
	mux.HandleFunc("DELETE /api/org/provider-connections/{id}", h.DeleteOrgProvider)
	// Dashboard SSE events
	mux.HandleFunc("GET /api/events", h.StreamDashboardEvents)
}
