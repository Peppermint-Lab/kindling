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

func assertHTTPForbidden(t *testing.T, rr *httptest.ResponseRecorder, label string) {
	t.Helper()
	if rr.Code != http.StatusForbidden {
		t.Fatalf("%s status = %d, want %d; body=%s", label, rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func testNonPlatformAdminPrincipal(orgRole string) auth.Principal {
	orgID := uuid.MustParse("c0000000-0000-4000-a000-000000000001")
	return auth.Principal{
		UserID:         uuid.MustParse("a0000000-0000-4000-a000-000000000001"),
		OrgID:          orgID,
		OrganizationID: pgtype.UUID{Bytes: orgID, Valid: true},
		OrgRole:        orgRole,
		PlatformAdmin:  false,
	}
}

func TestGetMeta_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	api := NewAPI(nil, nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/meta", nil)
	p := testNonPlatformAdminPrincipal("admin")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	api.getMeta(rr, req)
	assertHTTPForbidden(t, rr, "getMeta")
}

func TestPutMeta_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	api := NewAPI(nil, nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/meta", strings.NewReader(`{"public_base_url":"https://example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	p := testNonPlatformAdminPrincipal("owner")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	api.putMeta(rr, req)
	assertHTTPForbidden(t, rr, "putMeta")
}
