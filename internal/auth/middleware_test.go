package auth

import (
	"net/http/httptest"
	"testing"
)

func TestPublicRouteExternalAuthRoutes(t *testing.T) {
	t.Parallel()

	if !PublicRoute(httptest.NewRequest("GET", "/api/auth/providers", nil)) {
		t.Fatal("expected auth providers list to be public")
	}
	if !PublicRoute(httptest.NewRequest("GET", "/api/auth/providers/github/start", nil)) {
		t.Fatal("expected auth provider start route to be public")
	}
	if !PublicRoute(httptest.NewRequest("GET", "/api/auth/providers/github/callback", nil)) {
		t.Fatal("expected auth provider callback route to be public")
	}
	if PublicRoute(httptest.NewRequest("GET", "/api/auth/providers/github/link", nil)) {
		t.Fatal("expected auth provider link route to require a session")
	}
}
