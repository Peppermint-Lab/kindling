package rpc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (a *API) registerAPIKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/api-keys", a.listAPIKeys)
	mux.HandleFunc("POST /api/auth/api-keys", a.createAPIKey)
	mux.HandleFunc("DELETE /api/auth/api-keys/{id}", a.revokeAPIKey)
}

func apiKeyTimestamptzPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

func (a *API) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	keys, err := a.q.UserApiKeyListByUserAndOrg(r.Context(), queries.UserApiKeyListByUserAndOrgParams{
		UserID:         auth.PgUUID(p.UserID),
		OrganizationID: p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_api_keys", err)
		return
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		row := map[string]any{
			"id":              uuid.UUID(k.ID.Bytes).String(),
			"name":            k.Name,
			"organization_id": uuid.UUID(k.OrganizationID.Bytes).String(),
			"created_at":      k.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		}
		if lu := apiKeyTimestamptzPtr(k.LastUsedAt); lu != nil {
			row["last_used_at"] = *lu
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createAPIKey(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	var req struct {
		Name           string `json:"name"`
		OrganizationID string `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	orgID := p.OrgID
	if strings.TrimSpace(req.OrganizationID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(req.OrganizationID))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "organization_id must be a UUID")
			return
		}
		if _, err := a.q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
			UserID:         auth.PgUUID(p.UserID),
			OrganizationID: auth.PgUUID(parsed),
		}); err != nil {
			writeAPIError(w, http.StatusForbidden, "forbidden", "not a member of that organization")
			return
		}
		orgID = parsed
	}
	plain, hash, err := auth.NewAPIKeyToken()
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "token_generation", err)
		return
	}
	row, err := a.q.UserApiKeyCreate(r.Context(), queries.UserApiKeyCreateParams{
		ID:             auth.PgUUID(uuid.New()),
		UserID:         auth.PgUUID(p.UserID),
		OrganizationID: auth.PgUUID(orgID),
		Name:           name,
		TokenHash:      hash,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_api_key", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              uuid.UUID(row.ID.Bytes).String(),
		"name":            row.Name,
		"organization_id": uuid.UUID(row.OrganizationID.Bytes).String(),
		"token":           plain,
		"created_at":      row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	})
}

func (a *API) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	idStr := strings.TrimSpace(r.PathValue("id"))
	keyID, err := uuid.Parse(idStr)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid api key id")
		return
	}
	if err := a.q.UserApiKeyRevoke(r.Context(), queries.UserApiKeyRevokeParams{
		ID:     auth.PgUUID(keyID),
		UserID: auth.PgUUID(p.UserID),
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "revoke_api_key", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
