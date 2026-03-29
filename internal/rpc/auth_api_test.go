package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapRequestAllowedLoopback(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:1234"

	if !bootstrapRequestAllowed(req) {
		t.Fatal("expected direct loopback bootstrap request to be allowed")
	}
}

func TestBootstrapRequestAllowedIgnoresXFFForLoopbackPeer(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")

	// After the security fix: X-Forwarded-For is completely ignored for
	// loopback peers. The peer IS loopback, so bootstrap is allowed.
	if !bootstrapRequestAllowed(req) {
		t.Fatal("expected loopback peer to be allowed (XFF should be ignored)")
	}
}

func TestBootstrapRequestAllowedAcceptsConfiguredToken(t *testing.T) {
	t.Setenv(bootstrapTokenEnv, "bootstrap-secret")

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "203.0.113.20:443"
	req.Header.Set(bootstrapTokenHeader, "bootstrap-secret")

	if !bootstrapRequestAllowed(req) {
		t.Fatal("expected bootstrap token to allow remote bootstrap request")
	}
}

func TestBootstrapRequestAllowedRejectsRemoteWithoutToken(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "203.0.113.20:443"

	if bootstrapRequestAllowed(req) {
		t.Fatal("expected remote bootstrap request without token to be rejected")
	}
}

func TestAuthLogoutRejectsMissingOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://kindling.example.com/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	api := &API{}
	api.authLogout(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthLogoutAllowsSameOriginWithoutSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://kindling.example.com/api/auth/logout", nil)
	req.Header.Set("Origin", "https://kindling.example.com")
	rec := httptest.NewRecorder()

	api := &API{}
	api.authLogout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
