package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (a *API) registerProviderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/org/provider-connections", a.listOrgProviderConnections)
	mux.HandleFunc("POST /api/org/provider-connections", a.createOrgProviderConnection)
	mux.HandleFunc("DELETE /api/org/provider-connections/{id}", a.deleteOrgProviderConnection)
}

func providerToPublic(p queries.OrgProviderConnection) map[string]any {
	var meta json.RawMessage
	if len(p.Metadata) > 0 {
		meta = json.RawMessage(p.Metadata)
	} else {
		meta = json.RawMessage(`{}`)
	}
	return map[string]any{
		"id":              uuid.UUID(p.ID.Bytes).String(),
		"provider":        p.Provider,
		"external_slug":   p.ExternalSlug,
		"display_label":   p.DisplayLabel,
		"has_credentials": len(p.CredentialsCiphertext) > 0,
		"metadata":        meta,
		"created_at":      p.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"updated_at":      p.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (a *API) listOrgProviderConnections(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.q.OrgProviderConnectionListByOrg(r.Context(), p.OrganizationID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_provider_connections", err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i := range rows {
		out[i] = providerToPublic(rows[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createOrgProviderConnection(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if p.OrgRole != "owner" && p.OrgRole != "admin" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
		return
	}

	var req struct {
		Provider        string          `json:"provider"`
		ExternalSlug    string          `json:"external_slug"`
		DisplayLabel    string          `json:"display_label"`
		Token           string          `json:"token"`
		Metadata        json.RawMessage `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Provider = strings.TrimSpace(strings.ToLower(req.Provider))
	if req.Provider != "github" && req.Provider != "gitlab" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "provider must be github or gitlab")
		return
	}
	req.ExternalSlug = strings.TrimSpace(req.ExternalSlug)
	req.DisplayLabel = strings.TrimSpace(req.DisplayLabel)
	if req.DisplayLabel == "" {
		req.DisplayLabel = req.ExternalSlug
	}
	meta := []byte(`{}`)
	if len(req.Metadata) > 0 {
		meta = req.Metadata
	}

	var cipher []byte
	if strings.TrimSpace(req.Token) != "" && a.cfg != nil {
		var err error
		cipher, err = a.cfg.EncryptBytes([]byte(strings.TrimSpace(req.Token)))
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "encrypt_credentials", err)
			return
		}
	}

	row, err := a.q.OrgProviderConnectionCreate(r.Context(), queries.OrgProviderConnectionCreateParams{
		ID:                     pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrganizationID:         p.OrganizationID,
		Provider:               req.Provider,
		ExternalSlug:           req.ExternalSlug,
		DisplayLabel:           req.DisplayLabel,
		CredentialsCiphertext:  cipher,
		Metadata:               meta,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_provider_connection", err)
		return
	}
	writeJSON(w, http.StatusCreated, providerToPublic(row))
}

func (a *API) deleteOrgProviderConnection(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if p.OrgRole != "owner" && p.OrgRole != "admin" {
		writeAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid id")
		return
	}
	if err := a.q.OrgProviderConnectionDeleteByIDAndOrg(r.Context(), queries.OrgProviderConnectionDeleteByIDAndOrgParams{
		ID:             id,
		OrganizationID: p.OrganizationID,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "delete_provider_connection", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
