package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

func TestListCIWorkflows(t *testing.T) {
	t.Parallel()

	api := NewAPI(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/ci/workflows", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID:         uuid.New(),
		OrgID:          uuid.New(),
		OrgRole:        "admin",
		OrganizationID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
	}))
	rr := httptest.NewRecorder()
	api.listCIWorkflows(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"stem":"ci"`) {
		t.Fatalf("expected ci workflow in response, got %s", body)
	}
	if !strings.Contains(body, `"stem":"deploy-prod"`) {
		t.Fatalf("expected deploy-prod workflow in response, got %s", body)
	}
}

func TestCIJobToOutIncludesExecutionFields(t *testing.T) {
	t.Parallel()

	inputs, err := json.Marshal(map[string]string{"env": "prod"})
	if err != nil {
		t.Fatal(err)
	}
	out := ciJobToOut(queries.CiJob{
		ID:               pguuid.ToPgtype(uuid.New()),
		ProjectID:        pguuid.ToPgtype(uuid.New()),
		Status:           "running",
		Source:           "local_workflow_run",
		WorkflowName:     "deploy-prod",
		WorkflowFile:     ".github/workflows/deploy-prod.yml",
		SelectedJobID:    "deploy",
		EventName:        "workflow_dispatch",
		InputValues:      inputs,
		RequireMicrovm:   true,
		ExecutionBackend: "apple_vz",
	})
	if !out.RequireMicroVM {
		t.Fatal("expected require_microvm to be true")
	}
	if out.ExecutionBackend != "apple_vz" {
		t.Fatalf("expected execution backend apple_vz, got %q", out.ExecutionBackend)
	}
	if out.Inputs["env"] != "prod" {
		t.Fatalf("expected inputs to round-trip, got %#v", out.Inputs)
	}
}
