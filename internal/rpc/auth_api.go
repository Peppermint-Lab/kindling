package rpc

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	bootstrapTokenHeader = "X-Kindling-Bootstrap-Token"
	bootstrapTokenEnv    = "KINDLING_BOOTSTRAP_TOKEN"
)

func (a *API) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/bootstrap-status", a.authBootstrapStatus)
	mux.HandleFunc("GET /api/auth/session", a.authSession)
	mux.HandleFunc("POST /api/auth/bootstrap", a.authBootstrap)
	mux.HandleFunc("POST /api/auth/login", a.authLogin)
	mux.HandleFunc("POST /api/auth/logout", a.authLogout)
	mux.HandleFunc("POST /api/auth/switch-org", a.authSwitchOrg)
	a.registerExternalAuthRoutes(mux)
}

func (a *API) authBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	n, err := a.q.UserCount(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "bootstrap_status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_bootstrap":            n == 0,
		"bootstrap_token_configured": configuredBootstrapToken() != "",
	})
}

func (a *API) authSession(w http.ResponseWriter, r *http.Request) {
	out := a.sessionPayload(r)
	if v, ok := out["authenticated"].(bool); ok && v {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// This route is public (no middleware principal). Still honor API keys for CLI whoami/status.
	if apiOut := a.sessionPayloadFromAPIKey(r); apiOut != nil {
		writeJSON(w, http.StatusOK, apiOut)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func parseBearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(strings.TrimSpace(h), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func (a *API) sessionPayloadFromAPIKey(r *http.Request) map[string]any {
	tok, ok := parseBearerToken(r)
	if !ok || !strings.HasPrefix(tok, auth.APIKeyPrefix) {
		return nil
	}
	keyRow, err := a.q.UserApiKeyByTokenHash(r.Context(), auth.HashAPIKeyToken(tok))
	if err != nil {
		return nil
	}
	_ = a.q.UserApiKeyTouchLastUsed(r.Context(), keyRow.ID)
	u, err := a.q.UserByID(r.Context(), keyRow.UserID)
	if err != nil {
		return nil
	}
	mem, err := a.q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
		UserID:         keyRow.UserID,
		OrganizationID: keyRow.OrganizationID,
	})
	if err != nil {
		return nil
	}
	org, err := a.q.OrganizationByID(r.Context(), keyRow.OrganizationID)
	if err != nil {
		return nil
	}
	orgs, err := a.q.OrganizationsForUser(r.Context(), keyRow.UserID)
	if err != nil {
		slog.Warn("list orgs for api key session", "error", err)
		orgs = nil
	}
	return map[string]any{
		"authenticated": true,
		"user": map[string]any{
			"id":           uuid.UUID(u.ID.Bytes).String(),
			"email":        u.Email,
			"display_name": u.DisplayName,
		},
		"platform_admin": u.IsPlatformAdmin,
		"organization":   organizationJSON(org),
		"role":           mem.Role,
		"organizations": func() []any {
			sl := make([]any, 0, len(orgs))
			for _, o := range orgs {
				sl = append(sl, organizationJSON(o))
			}
			return sl
		}(),
		"auth": "api_key",
	}
}

func (a *API) authSuccessPayload(ctx context.Context, u queries.User, org queries.Organization, role string, allOrgs []queries.Organization) map[string]any {
	sl := make([]any, 0, len(allOrgs))
	for _, o := range allOrgs {
		sl = append(sl, organizationJSON(o))
	}
	return map[string]any{
		"authenticated": true,
		"user": map[string]any{
			"id":           uuid.UUID(u.ID.Bytes).String(),
			"email":        u.Email,
			"display_name": u.DisplayName,
		},
		"platform_admin": u.IsPlatformAdmin,
		"organization":   organizationJSON(org),
		"role":           role,
		"organizations":  sl,
	}
}

func (a *API) sessionPayload(r *http.Request) map[string]any {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return map[string]any{"authenticated": false}
	}
	raw, err := hex.DecodeString(strings.TrimSpace(cookie.Value))
	if err != nil || len(raw) != auth.SessionTokenBytes {
		return map[string]any{"authenticated": false}
	}
	sess, err := a.q.UserSessionByTokenHash(r.Context(), auth.HashSessionToken(raw))
	if err != nil {
		return map[string]any{"authenticated": false}
	}
	u, err := a.q.UserByID(r.Context(), sess.UserID)
	if err != nil {
		return map[string]any{"authenticated": false}
	}
	mem, err := a.q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
		UserID:         sess.UserID,
		OrganizationID: sess.CurrentOrganizationID,
	})
	if err != nil {
		return map[string]any{"authenticated": false}
	}
	org, err := a.q.OrganizationByID(r.Context(), sess.CurrentOrganizationID)
	if err != nil {
		return map[string]any{"authenticated": false}
	}
	orgs, err := a.q.OrganizationsForUser(r.Context(), sess.UserID)
	if err != nil {
		slog.Warn("list orgs for session", "error", err)
		orgs = nil
	}
	return map[string]any{
		"authenticated": true,
		"user": map[string]any{
			"id":           uuid.UUID(u.ID.Bytes).String(),
			"email":        u.Email,
			"display_name": u.DisplayName,
		},
		"platform_admin": u.IsPlatformAdmin,
		"organization":   organizationJSON(org),
		"role":           mem.Role,
		"organizations": func() []any {
			sl := make([]any, 0, len(orgs))
			for _, o := range orgs {
				sl = append(sl, organizationJSON(o))
			}
			return sl
		}(),
	}
}

func organizationJSON(o queries.Organization) map[string]any {
	return map[string]any{
		"id":   uuid.UUID(o.ID.Bytes).String(),
		"name": o.Name,
		"slug": o.Slug,
	}
}

func (a *API) authBootstrap(w http.ResponseWriter, r *http.Request) {
	n, err := a.q.UserCount(r.Context())
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "bootstrap", err)
		return
	}
	if n > 0 {
		writeAPIError(w, http.StatusForbidden, "bootstrap_done", "cluster already has users")
		return
	}
	if !bootstrapRequestAllowed(r) {
		writeAPIError(w, http.StatusForbidden, "bootstrap_forbidden", "bootstrap requires loopback access or a valid bootstrap token")
		return
	}

	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "valid email is required")
		return
	}
	if len(req.Password) < 8 {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "hash_password", err)
		return
	}

	userID := uuid.New()
	uRow, err := a.q.UserCreate(r.Context(), queries.UserCreateParams{
		ID:              pgtype.UUID{Bytes: userID, Valid: true},
		Email:           req.Email,
		PasswordHash:    hash,
		DisplayName:     req.DisplayName,
		IsPlatformAdmin: true,
	})
	if err != nil {
		if isPgUniqueViolation(err) {
			writeAPIError(w, http.StatusConflict, "email_taken", "that email is already registered")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "create_user", err)
		return
	}

	_, err = a.q.OrganizationMembershipCreate(r.Context(), queries.OrganizationMembershipCreateParams{
		ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OrganizationID: auth.PgUUID(auth.BootstrapOrganizationID),
		UserID:         pgtype.UUID{Bytes: userID, Valid: true},
		Role:           "owner",
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "membership", err)
		return
	}

	rawTok, orgRow, role, allOrgs, err := a.issueSessionForUser(r.Context(), uRow)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "session", err)
		return
	}
	auth.SetSessionCookie(w, r, rawTok, auth.RequestUsesHTTPS(r))
	writeJSON(w, http.StatusCreated, a.authSuccessPayload(r.Context(), uRow, orgRow, role, allOrgs))
}

func configuredBootstrapToken() string {
	return strings.TrimSpace(os.Getenv(bootstrapTokenEnv))
}

func bootstrapRequestAllowed(r *http.Request) bool {
	if token := configuredBootstrapToken(); token != "" {
		got := strings.TrimSpace(r.Header.Get(bootstrapTokenHeader))
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			return true
		}
	}

	peer, ok := parseRequestIP(r.RemoteAddr)
	if !ok {
		return false
	}

	// Non-loopback peers are never allowed without a token.
	if !peer.IsLoopback() {
		return false
	}

	// Peer is loopback. If ANY proxy header is present the request
	// arrived through the edge proxy (which forwards external traffic
	// to the API on 127.0.0.1:8080). In that case, require a token
	// regardless of what X-Forwarded-For says — XFF can be spoofed
	// by the external client since the edge proxy doesn't yet
	// sanitize it.
	if hasProxyHeaders(r) {
		return false
	}

	// Direct loopback connection (no proxy headers) — allow.
	return true
}

// bootstrapClientIP returns the effective client IP for bootstrap logging
// and diagnostics. It never trusts X-Forwarded-For; all bootstrap
// authorization decisions are made by bootstrapRequestAllowed above using
// the peer IP and proxy-header presence only.
func bootstrapClientIP(r *http.Request) (net.IP, bool) {
	peer, ok := parseRequestIP(r.RemoteAddr)
	if !ok {
		return nil, false
	}
	return peer, true
}

// hasProxyHeaders reports whether the request carries any standard proxy
// headers, indicating it arrived through a reverse proxy rather than as a
// direct connection.
func hasProxyHeaders(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Forwarded-Proto") != "" ||
		r.Header.Get("Via") != ""
}

func parseRequestIP(raw string) (net.IP, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	host := raw
	if strings.Contains(raw, ":") {
		if h, _, err := net.SplitHostPort(raw); err == nil {
			host = h
		}
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, false
	}
	return ip, true
}

func (a *API) authLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "email and password are required")
		return
	}

	u, err := a.q.UserByEmail(r.Context(), req.Email)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}

	rawTok, orgRow, role, orgs, err := a.issueSessionForUser(r.Context(), u)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "no_org", err.Error())
		return
	}
	auth.SetSessionCookie(w, r, rawTok, auth.RequestUsesHTTPS(r))
	writeJSON(w, http.StatusOK, a.authSuccessPayload(r.Context(), u, orgRow, role, orgs))
}

func (a *API) authLogout(w http.ResponseWriter, r *http.Request) {
	if !auth.RequestHasTrustedOrigin(r, auth.TrustedOrigins(r.Context(), a.q)) {
		writeAPIError(w, http.StatusForbidden, "csrf_forbidden", "request origin is not allowed")
		return
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil && cookie.Value != "" {
		if raw, err := hex.DecodeString(strings.TrimSpace(cookie.Value)); err == nil && len(raw) == auth.SessionTokenBytes {
			if sess, err := a.q.UserSessionByTokenHash(r.Context(), auth.HashSessionToken(raw)); err == nil {
				_ = a.q.UserSessionDelete(r.Context(), sess.ID)
			}
		}
	}
	auth.ClearSessionCookie(w, r, auth.RequestUsesHTTPS(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) authSwitchOrg(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if p.APIKeyID != uuid.Nil {
		writeAPIError(w, http.StatusBadRequest, "api_key_org_fixed", "organization is fixed for API keys; create another key for a different organization")
		return
	}
	var req struct {
		OrgID string `json:"organization_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	orgUUID, err := uuid.Parse(strings.TrimSpace(req.OrgID))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "organization_id must be a UUID")
		return
	}
	orgPg := auth.PgUUID(orgUUID)
	if _, err := a.q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
		UserID:         auth.PgUUID(p.UserID),
		OrganizationID: orgPg,
	}); err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "not a member of that organization")
		return
	}

	if _, err := a.q.UserSessionUpdateCurrentOrg(r.Context(), queries.UserSessionUpdateCurrentOrgParams{
		ID:                    auth.PgUUID(p.SessionID),
		CurrentOrganizationID: orgPg,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "switch_org", err)
		return
	}

	uRow, err := a.q.UserByID(r.Context(), auth.PgUUID(p.UserID))
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "user_reload", err)
		return
	}
	orgRow, err := a.q.OrganizationByID(r.Context(), orgPg)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "org_reload", err)
		return
	}
	mem, err := a.q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
		UserID:         auth.PgUUID(p.UserID),
		OrganizationID: orgPg,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "membership", err)
		return
	}
	allOrgs, err := a.q.OrganizationsForUser(r.Context(), auth.PgUUID(p.UserID))
	if err != nil {
		allOrgs = []queries.Organization{orgRow}
	}
	writeJSON(w, http.StatusOK, a.authSuccessPayload(r.Context(), uRow, orgRow, mem.Role, allOrgs))
}
