package servers

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

func testOrgAdminPrincipal() auth.Principal {
	orgID := uuid.MustParse("c0000000-0000-4000-a000-000000000001")
	return auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000002"),
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        "admin",
		PlatformAdmin:  false,
	}
}

func TestListServers_RequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/servers", nil)
	p := testOrgMemberPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.listServers(rr, req)
	assertHTTPForbidden(t, rr, "listServers")
}

func TestPostServerDrain_RequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+sid.String()+"/drain", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgMemberPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.postServerDrain(rr, req)
	assertHTTPForbidden(t, rr, "postServerDrain")
}

func TestPostServerDrain_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+sid.String()+"/drain", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgAdminPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.postServerDrain(rr, req)
	assertHTTPForbidden(t, rr, "postServerDrain non-platform")
}

func TestPostServerActivate_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+sid.String()+"/activate", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgAdminPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.postServerActivate(rr, req)
	assertHTTPForbidden(t, rr, "postServerActivate non-platform")
}

func TestGetServerDetails_RequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/servers/"+sid.String()+"/details", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgMemberPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.getServerDetails(rr, req)
	assertHTTPForbidden(t, rr, "getServerDetails")
}
