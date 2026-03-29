package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
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
