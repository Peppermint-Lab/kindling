package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
)

const deploymentSSEPollInterval = 2 * time.Second // SSE deployment stream poll interval

// Handler provides deployment API handlers.
type Handler struct {
	Q                    *queries.Queries
	DeploymentReconciler *reconciler.Scheduler
}

// RegisterRoutes mounts deployment routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/deployments", h.listDeployments)
	mux.HandleFunc("GET /api/deployments", h.listAllDeployments)
	mux.HandleFunc("GET /api/deployments/{id}", h.getDeployment)
	mux.HandleFunc("GET /api/deployments/{id}/logs", h.getDeploymentLogs)
	mux.HandleFunc("GET /api/deployments/{id}/stream", h.streamDeployment)
	mux.HandleFunc("POST /api/projects/{id}/deploy", h.triggerDeploy)
	mux.HandleFunc("POST /api/deployments/{id}/cancel", h.cancelDeployment)
}

func (h *Handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    id,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	deps, err := h.Q.DeploymentFindByProjectID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_deployments", err)
		return
	}
	out := make([]DeploymentOut, 0, len(deps))
	for _, d := range deps {
		out = append(out, h.ToOutCtx(r.Context(), d))
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) listAllDeployments(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	limit := int32(50)
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	rows, err := h.Q.DeploymentFindRecentWithProjectForOrg(r.Context(), queries.DeploymentFindRecentWithProjectForOrgParams{
		Limit: limit,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_deployments", err)
		return
	}
	out := make([]DeploymentListItemOut, len(rows))
	for i := range rows {
		out[i] = h.ListRowForOrgToOutCtx(r.Context(), rows[i])
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}
	dep, err := h.Q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, h.ToOutCtx(r.Context(), dep))
}

func (h *Handler) getDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}

	dep, err := h.Q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	if !dep.BuildID.Valid {
		rpcutil.WriteJSON(w, http.StatusOK, []any{})
		return
	}

	logs, err := h.Q.BuildLogsByBuildID(r.Context(), dep.BuildID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "build_logs", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, logs)
}

func (h *Handler) triggerDeploy(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}

	var req struct {
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_json", "malformed JSON body")
		return
	}
	if req.Commit == "" {
		req.Commit = "main"
	}

	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	dep, err := h.Q.DeploymentCreate(r.Context(), queries.DeploymentCreateParams{
		ID:                   pgtype.UUID{Bytes: uuid.New(), Valid: true},
		ProjectID:            projectID,
		GithubCommit:         req.Commit,
		GithubBranch:         req.Commit,
		DeploymentKind:       "production",
		PreviewEnvironmentID: pgtype.UUID{Valid: false},
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "create_deployment", err)
		return
	}

	rpcutil.WriteJSON(w, http.StatusCreated, h.ToOutCtx(r.Context(), dep))
}

func (h *Handler) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}
	dep, err := h.Q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	if err := h.Q.DeploymentUpdateFailedAt(r.Context(), id); err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "cancel_deployment", err)
		return
	}

	rpcutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func terminalPhase(phase string) bool {
	switch phase {
	case "running", "failed", "stopped":
		return true
	default:
		return false
	}
}

func (h *Handler) streamDeployment(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	id, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid deployment id")
		return
	}
	dep, err := h.Q.DeploymentFirstByID(r.Context(), id)
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if _, err := h.Q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    dep.ProjectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		rpcutil.WriteAPIError(w, http.StatusInternalServerError, "streaming_unsupported", "response does not support flushing")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(name string, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal sse event %s: %w", name, err)
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return fmt.Errorf("write sse event %s: %w", name, err)
		}
		flusher.Flush()
		return nil
	}

	var logs []queries.BuildLog
	if dep.BuildID.Valid {
		var logErr error
		logs, logErr = h.Q.BuildLogsByBuildID(r.Context(), dep.BuildID)
		if logErr != nil {
			slog.Warn("failed to fetch build logs for SSE stream", "build_id", dep.BuildID, "error", logErr)
		}
	}
	out := h.ToOutCtx(r.Context(), dep)
	if err := writeEvent("deployment", out); err != nil {
		return
	}
	for _, l := range logs {
		if err := writeEvent("logs", l); err != nil {
			return
		}
	}

	if terminalPhase(out.Phase) {
		_ = writeEvent("done", map[string]string{"phase": out.Phase})
		return
	}
	lastLogTS := pgtype.Timestamptz{}
	if len(logs) > 0 {
		lastLogTS = logs[len(logs)-1].CreatedAt
	}

	ticker := time.NewTicker(deploymentSSEPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			dep, err := h.Q.DeploymentFirstByID(r.Context(), id)
			if err != nil {
				return
			}
			newOut := h.ToOutCtx(r.Context(), dep)
			if err := writeEvent("deployment", newOut); err != nil {
				return
			}
			if dep.BuildID.Valid {
				var newLogs []queries.BuildLog
				var fetchErr error
				if lastLogTS.Valid {
					newLogs, fetchErr = h.Q.BuildLogsAfterCreatedAt(r.Context(), queries.BuildLogsAfterCreatedAtParams{
						BuildID:   dep.BuildID,
						CreatedAt: lastLogTS,
					})
				} else {
					newLogs, fetchErr = h.Q.BuildLogsByBuildID(r.Context(), dep.BuildID)
				}
				if fetchErr == nil {
					for _, l := range newLogs {
						if err := writeEvent("logs", l); err != nil {
							return
						}
						lastLogTS = l.CreatedAt
					}
				}
			}
			if terminalPhase(newOut.Phase) {
				_ = writeEvent("done", map[string]string{"phase": newOut.Phase})
				return
			}
		}
	}
}
