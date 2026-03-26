package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func terminalPhase(phase string) bool {
	switch phase {
	case "running", "failed", "stopped":
		return true
	default:
		return false
	}
}

func (a *API) streamDeployment(w http.ResponseWriter, r *http.Request) {
	principal, ok := mustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming_unsupported", "response does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(event string, payload any) error {
		b, mErr := json.Marshal(payload)
		if mErr != nil {
			return mErr
		}
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	dep, err := a.q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: principal.OrganizationID,
	}); err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	// Initial snapshot: full deployment + all logs (if build exists).
	out := a.deploymentToOutCtx(r.Context(), dep)
	if err := writeEvent("deployment", out); err != nil {
		return
	}
	var lastLogAt pgtype.Timestamptz
	lastLogAt = pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true}

	if dep.BuildID.Valid {
		logs, err := a.q.BuildLogsByBuildID(r.Context(), dep.BuildID)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "build_logs", err)
			return
		}
		if err := writeEvent("logs", logs); err != nil {
			return
		}
		if len(logs) > 0 {
			lastLogAt = logs[len(logs)-1].CreatedAt
		}
	}

	if terminalPhase(out.Phase) {
		_ = writeEvent("done", map[string]string{"reason": "terminal"})
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			dep, err := a.q.DeploymentFirstByID(r.Context(), id)
			if err != nil {
				return
			}
			out := a.deploymentToOutCtx(r.Context(), dep)
			if err := writeEvent("deployment", out); err != nil {
				return
			}

			if dep.BuildID.Valid {
				newLogs, err := a.q.BuildLogsAfterCreatedAt(r.Context(), queries.BuildLogsAfterCreatedAtParams{
					BuildID:   dep.BuildID,
					CreatedAt: lastLogAt,
				})
				if err != nil {
					return
				}
				if len(newLogs) > 0 {
					if err := writeEvent("logs", newLogs); err != nil {
						return
					}
					lastLogAt = newLogs[len(newLogs)-1].CreatedAt
				}
			}

			if terminalPhase(out.Phase) {
				_ = writeEvent("done", map[string]string{"reason": "terminal"})
				return
			}
		}
	}
}
