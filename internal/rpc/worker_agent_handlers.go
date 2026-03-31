package rpc

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func (a *API) registerWorkerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/worker/v1/enroll", a.workerV1Enroll)
	mux.HandleFunc("GET /api/worker/v1/desired-state", a.workerV1DesiredState)
	mux.HandleFunc("POST /api/worker/v1/status", a.workerV1Status)
	mux.HandleFunc("GET /install-kindling-worker.sh", a.installKindlingWorkerScript)
}

func wantsShellVars(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "env")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func writeShellVars(w http.ResponseWriter, vars map[string]string, order ...string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, key := range order {
		if value, ok := vars[key]; ok {
			_, _ = w.Write([]byte(key + "=" + shellQuote(value) + "\n"))
		}
	}
}

func (a *API) installKindlingWorkerScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(`#!/bin/sh
set -eu
# Hosted worker bootstrap: set KINDLING_CONTROL_PLANE to your API base URL (e.g. https://api.example.com).
# Set KINDLING_ENROLLMENT_TOKEN to the token from the dashboard.
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi
if [ -z "${KINDLING_CONTROL_PLANE:-}" ]; then
  echo "Set KINDLING_CONTROL_PLANE to the Kindling API base URL." >&2
  exit 1
fi
if [ -z "${KINDLING_ENROLLMENT_TOKEN:-}" ]; then
  echo "Set KINDLING_ENROLLMENT_TOKEN to the dashboard enrollment token." >&2
  exit 1
fi
BASE="${KINDLING_CONTROL_PLANE%/}"
HOSTNAME="${KINDLING_WORKER_HOSTNAME:-$(hostname)}"
ENROLL_ENV=$(curl -fsS -X POST "${BASE}/api/worker/v1/enroll?format=env" \
  -H "Content-Type: application/json" \
  -d "{\"enrollment_token\":\"${KINDLING_ENROLLMENT_TOKEN}\",\"hostname\":\"${HOSTNAME}\"}") || exit 1
eval "$ENROLL_ENV"
if [ -z "${api_token:-}" ]; then
  echo "enroll failed: missing api_token" >&2
  exit 1
fi
DESIRED_ENV=$(curl -fsS -H "Authorization: Bearer ${api_token}" \
  "${BASE}/api/worker/v1/desired-state?since_version=0&format=env") || exit 1
eval "$DESIRED_ENV"
if [ -z "${version:-}" ]; then
  echo "desired-state fetch failed: missing version" >&2
  exit 1
fi
curl -fsS -X POST "${BASE}/api/worker/v1/status" \
  -H "Authorization: Bearer ${api_token}" \
  -H "Content-Type: application/json" \
  -d "{\"desired_version_applied\":${version}}" >/dev/null
echo "Worker enrolled and reported desired-state version ${version}."
`))
}

func (a *API) workerV1Enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if DeploymentKind() != "hosted" {
		writeAPIError(w, http.StatusNotFound, "not_found", "hosted worker enrollment is not enabled for this deployment")
		return
	}
	var req struct {
		EnrollmentToken string `json:"enrollment_token"`
		WorkerPublicKey string `json:"worker_public_key"`
		Hostname        string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	tok := strings.TrimSpace(req.EnrollmentToken)
	if tok == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_token", "enrollment_token is required")
		return
	}
	plainAPI, hashAPI, err := auth.NewWorkerAgentToken()
	if err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "enroll", err)
		return
	}
	agentID := uuid.New()
	desiredPayload := []byte(`{"lifecycle":"active"}`)
	agent, err := a.q.WorkerAgentEnrollWithToken(r.Context(), queries.WorkerAgentEnrollWithTokenParams{
		TokenHash:              auth.HashWorkerEnrollmentToken(tok),
		ID:                     pgtype.UUID{Bytes: agentID, Valid: true},
		WorkerPublicKey:        strings.TrimSpace(req.WorkerPublicKey),
		ApiTokenHash:           hashAPI,
		Hostname:               strings.TrimSpace(req.Hostname),
		DesiredVersion:         1,
		DesiredStatePayload:    desiredPayload,
		ReportedVersionApplied: 0,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeAPIError(w, http.StatusUnauthorized, "invalid_token", "enrollment token is invalid or already used")
			return
		}
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "enroll", err)
		return
	}
	base := strings.TrimRight(publicAPIBaseFromRequest(r), "/")
	if wantsShellVars(r) {
		writeShellVars(w, map[string]string{
			"worker_id":       agentID.String(),
			"api_token":       plainAPI,
			"organization_id": uuid.UUID(agent.OrganizationID.Bytes).String(),
			"api_base_url":    base,
		}, "worker_id", "api_token", "organization_id", "api_base_url")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"worker_id":       agentID.String(),
		"api_token":       plainAPI,
		"organization_id": uuid.UUID(agent.OrganizationID.Bytes).String(),
		"api_base_url":    base,
	})
}

func (a *API) workerV1DesiredState(w http.ResponseWriter, r *http.Request) {
	wa, ok := mustWorkerAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	since := int32(0)
	if v := strings.TrimSpace(r.URL.Query().Get("since_version")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			since = int32(n)
		}
	}
	deadline := time.Now().Add(50 * time.Second)
	for {
		row, err := a.q.WorkerAgentByID(r.Context(), pgtype.UUID{Bytes: wa.AgentID, Valid: true})
		if err != nil {
			if err == pgx.ErrNoRows {
				writeAPIError(w, http.StatusNotFound, "not_found", "worker agent not found")
				return
			}
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "desired_state", err)
			return
		}
		if row.DesiredVersion > since || time.Now().After(deadline) {
			var payload map[string]any
			if len(row.DesiredStatePayload) > 0 {
				_ = json.Unmarshal(row.DesiredStatePayload, &payload)
			}
			if payload == nil {
				payload = map[string]any{}
			}
			if wantsShellVars(r) {
				stateJSON := "{}"
				if len(row.DesiredStatePayload) > 0 {
					stateJSON = string(row.DesiredStatePayload)
				}
				writeShellVars(w, map[string]string{
					"version":    strconv.FormatInt(int64(row.DesiredVersion), 10),
					"state_json": stateJSON,
				}, "version", "state_json")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"version": row.DesiredVersion,
				"state":   payload,
			})
			return
		}
		select {
		case <-r.Context().Done():
			writeAPIError(w, http.StatusRequestTimeout, "cancelled", "client disconnected")
			return
		case <-time.After(1 * time.Second):
		}
	}
}

func (a *API) workerV1Status(w http.ResponseWriter, r *http.Request) {
	wa, ok := mustWorkerAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req struct {
		DesiredVersionApplied int32 `json:"desired_version_applied"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if err := a.q.WorkerAgentSetReportedVersion(r.Context(), queries.WorkerAgentSetReportedVersionParams{
		ID:                     pgtype.UUID{Bytes: wa.AgentID, Valid: true},
		ReportedVersionApplied: req.DesiredVersionApplied,
	}); err != nil {
		writeAPIErrorFromErr(w, http.StatusInternalServerError, "status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
