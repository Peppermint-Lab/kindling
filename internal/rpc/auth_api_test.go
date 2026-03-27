package rpc

import (
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

func TestBootstrapRequestAllowedRejectsForwardedRemoteClient(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/auth/bootstrap", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.5")

	if bootstrapRequestAllowed(req) {
		t.Fatal("expected proxied remote bootstrap request without token to be rejected")
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
