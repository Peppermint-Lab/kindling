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

type sandboxAccessEventOut struct {
	ID           string `json:"id"`
	SandboxID    string `json:"sandbox_id"`
	UserID       string `json:"user_id,omitempty"`
	UserEmail    string `json:"user_email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	AccessMethod string `json:"access_method"`
	EventType    string `json:"event_type"`
	ExitCode     *int32 `json:"exit_code,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func sandboxAccessEventToOut(row queries.SandboxAccessEventsBySandboxIDAndOrgRow) sandboxAccessEventOut {
	out := sandboxAccessEventOut{
		ID:           optionalUUIDStringOrZero(row.ID),
		SandboxID:    optionalUUIDStringOrZero(row.SandboxID),
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

func (a *API) recordSandboxAccessEvent(ctx context.Context, sandboxID, userID uuid.UUID, method, eventType string, exitCode *int, errorSummary string) {
	if a == nil || a.q == nil || sandboxID == uuid.Nil {
		return
	}
	row := queries.SandboxAccessEventCreateParams{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		SandboxID:    pgtype.UUID{Bytes: sandboxID, Valid: true},
		UserID:       pgtype.UUID{Bytes: userID, Valid: userID != uuid.Nil},
		AccessMethod: method,
		EventType:    eventType,
		ErrorSummary: strings.TrimSpace(errorSummary),
	}
	if exitCode != nil {
		row.ExitCode = pgtype.Int4{Int32: int32(*exitCode), Valid: true}
	}
	_, _ = a.q.SandboxAccessEventCreate(ctx, row)
}

func (a *API) listSandboxAccessEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid sandbox id")
		return
	}
	rows, err := a.q.SandboxAccessEventsBySandboxIDAndOrg(r.Context(), queries.SandboxAccessEventsBySandboxIDAndOrgParams{
		SandboxID: id,
		OrgID:     p.OrganizationID,
	})
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "list_sandbox_access_events", err)
		return
	}
	out := make([]sandboxAccessEventOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, sandboxAccessEventToOut(row))
	}
	writeJSON(w, http.StatusOK, out)
}
