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
	labels, err := json.Marshal([]string{"self-hosted", "kindling", "linux", "x64"})
	if err != nil {
		t.Fatal(err)
	}
	out := ciJobToOut(queries.CiJob{
		ID:                     pguuid.ToPgtype(uuid.New()),
		ProjectID:              pguuid.ToPgtype(uuid.New()),
		Status:                 "running",
		Source:                 "github_actions_runner",
		WorkflowName:           "deploy-prod",
		WorkflowFile:           ".github/workflows/deploy-prod.yml",
		SelectedJobID:          "deploy",
		EventName:              "workflow_dispatch",
		InputValues:            inputs,
		ProviderConnectionID:   pguuid.ToPgtype(uuid.New()),
		ExternalRepo:           "kindlingvm/kindling",
		ExternalInstallationID: 42,
		ExternalWorkflowJobID:  101,
		ExternalWorkflowRunID:  202,
		ExternalRunAttempt:     2,
		ExternalHtmlUrl:        "https://github.com/kindlingvm/kindling/actions/runs/202/job/101",
		RunnerLabels:           labels,
		RunnerName:             "kindling-202-101",
		RequireMicrovm:         true,
		ExecutionBackend:       "apple_vz",
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
	if out.ExternalRepo != "kindlingvm/kindling" {
		t.Fatalf("expected external repo, got %q", out.ExternalRepo)
	}
	if len(out.RunnerLabels) != 4 {
		t.Fatalf("expected runner labels to round-trip, got %#v", out.RunnerLabels)
	}
	if out.RunnerName != "kindling-202-101" {
		t.Fatalf("expected runner name, got %q", out.RunnerName)
	}
}

func TestCIJobToOutWithProjectName(t *testing.T) {
	t.Parallel()

	out := ciJobToOutWithProjectName(queries.CiJob{
		ID:        pguuid.ToPgtype(uuid.New()),
		ProjectID: pguuid.ToPgtype(uuid.New()),
		Status:    "queued",
		Source:    "local_workflow_run",
	}, "demo-app")
	if out.ProjectName != "demo-app" {
		t.Fatalf("expected project name to be included, got %q", out.ProjectName)
	}
}
