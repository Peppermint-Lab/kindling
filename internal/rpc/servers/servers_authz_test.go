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

func TestGetPlatformServerDetails_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/platform/servers/"+sid.String()+"/details", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgAdminPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.getPlatformServerDetails(rr, req)
	assertHTTPForbidden(t, rr, "getPlatformServerDetails")
}

func TestListPlatformServers_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/platform/servers", nil)
	p := testOrgAdminPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.listPlatformServers(rr, req)
	assertHTTPForbidden(t, rr, "listPlatformServers")
}

func TestGetPlatformHealthOverview_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/platform/health/overview", nil)
	p := testOrgAdminPrincipal()
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.getPlatformHealthOverview(rr, req)
	assertHTTPForbidden(t, rr, "getPlatformHealthOverview")
}

func TestGetServerDetails_PlatformAdminStillRequiresOrgAdmin(t *testing.T) {
	t.Parallel()
	h := &Handler{Q: nil}
	rr := httptest.NewRecorder()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/servers/"+sid.String()+"/details", nil)
	req.SetPathValue("id", sid.String())
	p := testOrgMemberPrincipal()
	p.PlatformAdmin = true
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.getServerDetails(rr, req)
	assertHTTPForbidden(t, rr, "getServerDetails platform member")
}

func TestPlatformServerHandlers_PlatformAdminPassesAuthThenPanicsWithoutQ(t *testing.T) {
	t.Parallel()
	sid := uuid.MustParse("d0000000-0000-4000-a000-000000000001")
	cases := []struct {
		name   string
		newReq func() *http.Request
		run    func(*Handler, *httptest.ResponseRecorder, *http.Request)
	}{
		{
			name: "getPlatformServerDetails",
			newReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/platform/servers/"+sid.String()+"/details", nil)
				req.SetPathValue("id", sid.String())
				return req
			},
			run: func(h *Handler, rr *httptest.ResponseRecorder, req *http.Request) {
				h.getPlatformServerDetails(rr, req)
			},
		},
		{
			name: "listPlatformServers",
			newReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/platform/servers", nil)
			},
			run: func(h *Handler, rr *httptest.ResponseRecorder, req *http.Request) {
				h.listPlatformServers(rr, req)
			},
		},
		{
			name: "getPlatformHealthOverview",
			newReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/platform/health/overview", nil)
			},
			run: func(h *Handler, rr *httptest.ResponseRecorder, req *http.Request) {
				h.getPlatformHealthOverview(rr, req)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &Handler{Q: nil}
			rr := httptest.NewRecorder()
			req := tc.newReq()
			p := testOrgMemberPrincipal()
			p.PlatformAdmin = true
			req = req.WithContext(auth.WithPrincipal(req.Context(), p))
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic from nil Q after auth passed")
				}
			}()
			tc.run(h, rr, req)
		})
	}
}
