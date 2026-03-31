package deployments

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
)

func assertHTTPForbidden(t *testing.T, rr *httptest.ResponseRecorder, label string) {
	t.Helper()
	if rr.Code != http.StatusForbidden {
		t.Fatalf("%s status = %d, want %d; body=%s", label, rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func testOrgMemberPrincipal() auth.Principal {
	orgID := uuid.MustParse("c0000000-0000-4000-a000-000000000001")
	return auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000001"),
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "member",
	}
}

func TestGetDeploymentInstanceMigration_RequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	instID := uuid.MustParse("e0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/deployment-instances/"+instID.String()+"/migration", nil)
	req.SetPathValue("id", instID.String())
	p := testOrgMemberPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.getDeploymentInstanceMigration(rr, req)
	assertHTTPForbidden(t, rr, "getDeploymentInstanceMigration")
}

func TestPostDeploymentInstanceLiveMigrate_RequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	instID := uuid.MustParse("e0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodPost, "/api/deployment-instances/"+instID.String()+"/live-migrate", nil)
	req.SetPathValue("id", instID.String())
	p := testOrgMemberPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.postDeploymentInstanceLiveMigrate(rr, req)
	assertHTTPForbidden(t, rr, "postDeploymentInstanceLiveMigrate")
}
