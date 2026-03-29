package rpc

import (
	"net/http"
	"strings"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (a *API) registerPendingMemberRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/org/pending-members", a.listPendingMembers)
	mux.HandleFunc("POST /api/org/pending-members/{user_id}/approve", a.approvePendingMember)
	mux.HandleFunc("POST /api/org/pending-members/{user_id}/reject", a.rejectPendingMember)
}

func (a *API) listPendingMembers(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	rows, err := a.q.OrganizationMembershipListPendingByOrg(r.Context(), p.OrganizationID)
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_pending_members", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"user_id":      row.UserID,
			"email":        row.Email,
			"display_name": row.DisplayName,
			"role":         row.Role,
			"status":       row.Status,
			"created_at":   timestampString(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) approvePendingMember(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	userID, err := parseUUID(strings.TrimSpace(r.PathValue("user_id")))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "invalid user ID")
		return
	}
	membership, err := a.q.OrganizationMembershipUpdateStatus(r.Context(), queries.OrganizationMembershipUpdateStatusParams{
		OrganizationID: p.OrganizationID,
		UserID:         userID,
		Status:         "active",
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusNotFound, "approve_member", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": membership.UserID,
		"status":  membership.Status,
		"role":    membership.Role,
	})
}

func (a *API) rejectPendingMember(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	if !requireOrgAdmin(w, p) {
		return
	}
	userID, err := parseUUID(strings.TrimSpace(r.PathValue("user_id")))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_user_id", "invalid user ID")
		return
	}
	membership, err := a.q.OrganizationMembershipUpdateStatus(r.Context(), queries.OrganizationMembershipUpdateStatusParams{
		OrganizationID: p.OrganizationID,
		UserID:         userID,
		Status:         "rejected",
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusNotFound, "reject_member", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": membership.UserID,
		"status":  membership.Status,
		"role":    membership.Role,
	})
}


