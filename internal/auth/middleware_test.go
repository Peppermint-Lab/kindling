package auth

import (
	"net/http"
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

func TestCookieRequestHasTrustedOriginAllowsSameOrigin(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "https://kindling.example.com/api/projects", nil)
	req.Header.Set("Origin", "https://kindling.example.com")

	if !cookieRequestHasTrustedOrigin(req) {
		t.Fatal("expected same-origin request to be allowed")
	}
}

func TestCookieRequestHasTrustedOriginRejectsSiblingSubdomain(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "https://kindling.example.com/api/projects", nil)
	req.Header.Set("Origin", "https://preview.kindling.example.com")

	if cookieRequestHasTrustedOrigin(req) {
		t.Fatal("expected sibling subdomain origin to be rejected")
	}
}

func TestRequestHasTrustedOriginAllowsConfiguredDashboardOrigin(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "https://api.kindling.example.com/api/projects", nil)
	req.Header.Set("Origin", "https://app.kindling.example.com")

	if !RequestHasTrustedOrigin(req, []string{"https://app.kindling.example.com"}) {
		t.Fatal("expected configured dashboard origin to be allowed")
	}
}

func TestCookieRequestHasTrustedOriginAllowsMatchingReferer(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodDelete, "https://kindling.example.com/api/projects/123", nil)
	req.Header.Set("Referer", "https://kindling.example.com/settings?tab=projects")

	if !cookieRequestHasTrustedOrigin(req) {
		t.Fatal("expected same-origin referer to be allowed")
	}
}

func TestCookieRequestHasTrustedOriginRejectsMissingHeadersOnWrite(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "https://kindling.example.com/api/projects", nil)

	if cookieRequestHasTrustedOrigin(req) {
		t.Fatal("expected write without Origin/Referer to be rejected")
	}
}

func TestRequestHasTrustedOriginAllowsLoopbackCrossPort(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/projects", nil)
	req.Header.Set("Origin", "http://localhost:5173")

	if !RequestHasTrustedOrigin(req, nil) {
		t.Fatal("expected loopback cross-port origin to be allowed")
	}
}

func TestCookieRequestHasTrustedOriginSkipsSafeMethods(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "https://kindling.example.com/api/projects", nil)

	if !cookieRequestHasTrustedOrigin(req) {
		t.Fatal("expected safe method to skip origin enforcement")
	}
}
