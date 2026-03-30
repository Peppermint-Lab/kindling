package rpc

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

type remoteVMAccessEventOut struct {
	ID           string `json:"id"`
	RemoteVmID   string `json:"remote_vm_id"`
	UserID       string `json:"user_id,omitempty"`
	UserEmail    string `json:"user_email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	AccessMethod string `json:"access_method"`
	EventType    string `json:"event_type"`
	ExitCode     *int32 `json:"exit_code,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func remoteVMAccessEventToOut(row queries.RemoteVMAccessEventsByRemoteVMIDAndOrgRow) remoteVMAccessEventOut {
	out := remoteVMAccessEventOut{
		ID:           optionalUUIDStringOrZero(row.ID),
		RemoteVmID:   optionalUUIDStringOrZero(row.RemoteVmID),
		UserID:       optionalUUIDStringOrZero(row.UserID),
		UserEmail:    strings.TrimSpace(row.Email.String),
		DisplayName:  strings.TrimSpace(row.DisplayName.String),
		AccessMethod: row.AccessMethod,
		EventType:    row.EventType,
		ErrorSummary: strings.TrimSpace(row.ErrorSummary),
		CreatedAt:    row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
	if row.ExitCode.Valid {
		v := row.ExitCode.Int32
		out.ExitCode = &v
	}
	return out
}

func optionalUUIDStringOrZero(v pgtype.UUID) string {
	if !v.Valid {
		return ""
	}
	return uuid.UUID(v.Bytes).String()
}

func (a *API) recordRemoteVMAccessEvent(ctx context.Context, remoteVmID, userID uuid.UUID, method, eventType string, exitCode *int, errorSummary string) {
	if a == nil || a.q == nil || remoteVmID == uuid.Nil {
		return
	}
	row := queries.RemoteVMAccessEventCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		RemoteVmID:   pgtype.UUID{Bytes: remoteVmID, Valid: true},
		UserID:       pgtype.UUID{Bytes: userID, Valid: userID != uuid.Nil},
		AccessMethod: method,
		EventType:    eventType,
		ErrorSummary: strings.TrimSpace(errorSummary),
	}
	if exitCode != nil {
		row.ExitCode = pgtype.Int4{Int32: int32(*exitCode), Valid: true}
	}
	_, _ = a.q.RemoteVMAccessEventCreate(ctx, row)
}

func (a *API) listRemoteVMAccessEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid remote vm id")
		return
	}
	rows, err := a.q.RemoteVMAccessEventsByRemoteVMIDAndOrg(r.Context(), queries.RemoteVMAccessEventsByRemoteVMIDAndOrgParams{
		RemoteVmID: id,
		OrgID:      p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_remote_vm_access_events", err)
		return
	}
	out := make([]remoteVMAccessEventOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, remoteVMAccessEventToOut(row))
	}
	writeJSON(w, http.StatusOK, out)
}
