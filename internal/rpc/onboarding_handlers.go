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
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func (a *API) registerOnboardingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/onboarding", a.getOnboarding)
	mux.HandleFunc("PATCH /api/onboarding", a.patchOnboarding)
	mux.HandleFunc("POST /api/onboarding/complete", a.postOnboardingComplete)
	mux.HandleFunc("POST /api/onboarding/worker-enrollment-token", a.postWorkerEnrollmentToken)
}

func onboardingCanEdit(role string, platformAdmin bool) bool {
	if platformAdmin {
		return true
	}
	return role == "owner" || role == "admin"
}

func onboardingStateOrgID(currentOrgID pgtype.UUID) pgtype.UUID {
	if DeploymentKind() == "self_hosted" {
		return auth.PgUUID(auth.BootstrapOrganizationID)
	}
	return currentOrgID
}

func onboardingCanEditForDeployment(role string, platformAdmin bool) bool {
	if DeploymentKind() == "self_hosted" {
		return platformAdmin
	}
	return onboardingCanEdit(role, platformAdmin)
}

func onboardingEditDeniedMessage() string {
	if DeploymentKind() == "self_hosted" {
		return "platform admin required to update self-hosted cluster onboarding"
	}
	return "only owners and admins can update onboarding"
}

func onboardingCompleteDeniedMessage() string {
	if DeploymentKind() == "self_hosted" {
		return "platform admin required to complete self-hosted cluster onboarding"
	}
	return "only owners and admins can complete onboarding"
}

func (a *API) getOnboarding(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	orgID := onboardingStateOrgID(p.OrganizationID)
	_ = a.q.OrgOnboardingEnsure(r.Context(), orgID)
	row, err := a.q.OrgOnboardingGet(r.Context(), orgID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "onboarding", err)
		return
	}
	var wizard map[string]any
	if len(row.WizardState) > 0 {
		_ = json.Unmarshal(row.WizardState, &wizard)
	}
	if wizard == nil {
		wizard = map[string]any{}
	}

	out := map[string]any{
		"deployment_kind":      DeploymentKind(),
		"onboarding_completed": row.CompletedAt.Valid,
		"wizard_state":         wizard,
		"can_edit":             onboardingCanEditForDeployment(p.OrgRole, p.PlatformAdmin),
	}

	switch DeploymentKind() {
	case "hosted":
		n, err := a.q.WorkerAgentCountHealthyByOrg(r.Context(), orgID)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "worker_agents", err)
			return
		}
		out["healthy_worker_agents"] = n
		agents, err := a.q.WorkerAgentListByOrg(r.Context(), orgID)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "worker_agents", err)
			return
		}
		list := make([]map[string]any, 0, len(agents))
		for _, wa := range agents {
			list = append(list, map[string]any{
				"id":                        pguuid.ToString(wa.ID),
				"hostname":                  wa.Hostname,
				"last_seen_at":              nullTimeRFC3339(wa.LastSeenAt),
				"desired_version":           wa.DesiredVersion,
				"reported_version_applied":  wa.ReportedVersionApplied,
			})
		}
		out["worker_agents"] = list
	default:
		servers, err := a.q.ServerFindAll(r.Context())
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "servers", err)
			return
		}
		out["servers_count"] = len(servers)
		healthy := int64(0)
		for _, s := range servers {
			if s.LastHeartbeatAt.Valid && time.Since(s.LastHeartbeatAt.Time) < 3*time.Minute {
				healthy++
			}
		}
		out["healthy_servers"] = healthy
	}

	writeJSON(w, http.StatusOK, out)
}

func nullTimeRFC3339(t pgtype.Timestamptz) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func (a *API) patchOnboarding(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPatch {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "PATCH required")
		return
	}
	if !onboardingCanEditForDeployment(p.OrgRole, p.PlatformAdmin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", onboardingEditDeniedMessage())
		return
	}
	var req struct {
		WizardState map[string]any `json:"wizard_state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	b, err := json.Marshal(req.WizardState)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_state", "wizard_state must be JSON-serializable")
		return
	}
	orgID := onboardingStateOrgID(p.OrganizationID)
	_ = a.q.OrgOnboardingEnsure(r.Context(), orgID)
	if err := a.q.OrgOnboardingUpdateWizardState(r.Context(), queries.OrgOnboardingUpdateWizardStateParams{
		OrganizationID: orgID,
		WizardState:    b,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "onboarding", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) postOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if !onboardingCanEditForDeployment(p.OrgRole, p.PlatformAdmin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", onboardingCompleteDeniedMessage())
		return
	}
	orgID := onboardingStateOrgID(p.OrganizationID)
	_ = a.q.OrgOnboardingEnsure(r.Context(), orgID)
	row, err := a.q.OrgOnboardingGet(r.Context(), orgID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "onboarding", err)
		return
	}
	if row.CompletedAt.Valid {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_completed"})
		return
	}

	switch DeploymentKind() {
	case "hosted":
		n, err := a.q.WorkerAgentCountHealthyByOrg(r.Context(), orgID)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "onboarding", err)
			return
		}
		if n < 1 {
			writeAPIError(w, http.StatusBadRequest, "workers_not_ready", "at least one healthy worker agent is required before completing onboarding")
			return
		}
	default:
		if !p.PlatformAdmin {
			writeAPIError(w, http.StatusForbidden, "forbidden", "platform admin required to complete self-hosted cluster onboarding")
			return
		}
		servers, err := a.q.ServerFindAll(r.Context())
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "servers", err)
			return
		}
		if len(servers) == 0 {
			writeAPIError(w, http.StatusBadRequest, "no_servers", "at least one registered server is required before completing onboarding")
			return
		}
		okHealth := false
		for _, s := range servers {
			if s.LastHeartbeatAt.Valid && time.Since(s.LastHeartbeatAt.Time) < 3*time.Minute {
				okHealth = true
				break
			}
		}
		if !okHealth {
			writeAPIError(w, http.StatusBadRequest, "servers_not_healthy", "at least one server must have a recent heartbeat")
			return
		}
	}

	if err := a.q.OrgOnboardingComplete(r.Context(), orgID); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "onboarding", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}

func (a *API) postWorkerEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if DeploymentKind() != "hosted" {
		writeAPIError(w, http.StatusBadRequest, "not_hosted", "worker enrollment tokens are only used in hosted deployments")
		return
	}
	if !onboardingCanEdit(p.OrgRole, p.PlatformAdmin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "only owners and admins can create enrollment tokens")
		return
	}
	plain, hash, err := auth.NewWorkerEnrollmentToken()
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "token", err)
		return
	}
	tokID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	expires := time.Now().UTC().Add(24 * time.Hour)
	_, err = a.q.WorkerEnrollmentTokenInsert(r.Context(), queries.WorkerEnrollmentTokenInsertParams{
		ID:             tokID,
		OrganizationID: p.OrganizationID,
		TokenHash:      hash,
		ExpiresAt:      pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "token", err)
		return
	}

	base := strings.TrimRight(publicAPIBaseFromRequest(r), "/")
	install := "curl -fsSL " + base + "/install-kindling-worker.sh | KINDLING_CONTROL_PLANE=" + base + " KINDLING_ENROLLMENT_TOKEN=" + plain + " sh"
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollment_token": plain,
		"expires_at":       expires.Format(time.RFC3339),
		"api_base_url":     base,
		"install_hint":     install,
	})
}

func publicAPIBaseFromRequest(r *http.Request) string {
	proto := "http"
	if auth.RequestUsesHTTPS(r) {
		proto = "https"
	}
	host := strings.TrimSpace(r.Host)
	return proto + "://" + host
}
