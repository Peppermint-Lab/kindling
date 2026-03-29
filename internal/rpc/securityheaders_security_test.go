package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/shared/httputil"
)

// expectedSecurityHeaders lists the security headers that MUST appear on every
// response (both success and error). HSTS is conditional on HTTPS.
var expectedSecurityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
}

// securityHeadersMiddleware wraps a handler with the security headers
// middleware defined in cmd/kindling. We import only the logic here via
// a package-local helper that mirrors production behaviour.
func testSecurityHeadersHandler(inner http.Handler) http.Handler {
	return SecurityHeadersMiddleware(inner)
}

// assertSecurityHeaders verifies all expected security headers are present
// on the recorder's response.
func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	for hdr, want := range expectedSecurityHeaders {
		got := rec.Header().Get(hdr)
		if got != want {
			t.Errorf("[%s] %s = %q, want %q", label, hdr, got, want)
		}
	}
}

// assertHSTSPresent asserts that Strict-Transport-Security is set with a
// positive max-age value.
func assertHSTSPresent(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	hsts := rec.Header().Get("Strict-Transport-Security")
	if hsts == "" {
		t.Fatalf("[%s] Strict-Transport-Security header missing", label)
	}
	if !strings.Contains(hsts, "max-age=") {
		t.Fatalf("[%s] HSTS missing max-age directive: %q", label, hsts)
	}
	if strings.Contains(hsts, "max-age=0") {
		t.Fatalf("[%s] HSTS max-age must be positive, got: %q", label, hsts)
	}
}

// assertHSTSAbsent asserts Strict-Transport-Security is NOT set (HTTP only).
func assertHSTSAbsent(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	if hsts := rec.Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Fatalf("[%s] HSTS should be absent on plain HTTP, got: %q", label, hsts)
	}
}

// ---------- VAL-HEADERS-001: Baseline security headers on HTTPS API responses ----------

func TestSecurityHeaders_BaselineOnSuccessResponse(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	handler := testSecurityHeadersHandler(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/bootstrap-status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertSecurityHeaders(t, rec, "success-200")
}

// ---------- VAL-HEADERS-002: Strict-Transport-Security on HTTPS responses ----------

func TestSecurityHeaders_HSTSOnHTTPS(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := testSecurityHeadersHandler(inner)

	// Simulate HTTPS request via X-Forwarded-Proto (edge proxy sets this).
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assertSecurityHeaders(t, rec, "https-hsts")
	assertHSTSPresent(t, rec, "https-hsts")
}

func TestSecurityHeaders_NoHSTSOnHTTP(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := testSecurityHeadersHandler(inner)

	// Plain HTTP (no TLS, no X-Forwarded-Proto).
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assertSecurityHeaders(t, rec, "http-no-hsts")
	assertHSTSAbsent(t, rec, "http-no-hsts")
}

// ---------- VAL-HEADERS-003: Security headers applied across all route types ----------

func TestSecurityHeaders_PresentOnPublicAndProtectedRoutes(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	handler := testSecurityHeadersHandler(inner)

	routes := []struct {
		method string
		path   string
		label  string
	}{
		{http.MethodGet, "/api/auth/bootstrap-status", "public-bootstrap-status"},
		{http.MethodPost, "/api/auth/login", "public-login"},
		{http.MethodGet, "/api/projects", "protected-list-projects"},
		{http.MethodGet, "/healthz", "public-healthz"},
	}

	for _, tc := range routes {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assertSecurityHeaders(t, rec, tc.label)
		})
	}
}

// ---------- VAL-HEADERS-005: Security headers present on error responses ----------

func TestSecurityHeaders_PresentOnErrorResponses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		code   string
		msg    string
	}{
		{"401-unauthorized", http.StatusUnauthorized, "unauthorized", "unauthorized"},
		{"403-forbidden", http.StatusForbidden, "csrf_forbidden", "request origin is not allowed"},
		{"413-payload-too-large", http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large"},
		{"429-rate-limited", http.StatusTooManyRequests, "rate_limited", "too many requests"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httputil.WriteAPIError(w, tc.status, tc.code, tc.msg)
			})
			handler := testSecurityHeadersHandler(inner)

			req := httptest.NewRequest(http.MethodPost, "/api/some/route", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			assertSecurityHeaders(t, rec, tc.name)
		})
	}
}

// ---------- VAL-CROSS-001: Rate-limited responses include security headers ----------

func TestSecurityHeaders_OnRateLimitedResponses(t *testing.T) {
	t.Parallel()

	// Simulate a rate-limited handler returning 429.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		httputil.WriteAPIError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
	})
	handler := testSecurityHeadersHandler(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	assertSecurityHeaders(t, rec, "rate-limited-429")

	// Also confirm the Retry-After header is still present (not clobbered).
	if ra := rec.Header().Get("Retry-After"); ra != "60" {
		t.Fatalf("Retry-After = %q, want %q", ra, "60")
	}
}

// ---------- HSTS includes includeSubDomains directive ----------

func TestSecurityHeaders_HSTSIncludesSubDomains(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := testSecurityHeadersHandler(inner)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	hsts := rec.Header().Get("Strict-Transport-Security")
	if !strings.Contains(hsts, "includeSubDomains") {
		t.Fatalf("HSTS %q should contain includeSubDomains", hsts)
	}
}

// ---------- Security headers do NOT overwrite handler headers ----------

func TestSecurityHeaders_DoNotClobberHandlerHeaders(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "custom-value")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	})
	handler := testSecurityHeadersHandler(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Security headers must be present.
	assertSecurityHeaders(t, rec, "no-clobber")

	// Handler's own headers must be preserved.
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want %q (handler header clobbered)", ct, "text/plain")
	}
	if xc := rec.Header().Get("X-Custom-Header"); xc != "custom-value" {
		t.Fatalf("X-Custom-Header = %q, want %q (handler header clobbered)", xc, "custom-value")
	}
}

// ---------- RequestUsesHTTPS is used for HSTS detection ----------

func TestSecurityHeaders_HSTSViaDirectTLS(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := testSecurityHeadersHandler(inner)

	// Simulate direct TLS connection.
	req := httptest.NewRequest(http.MethodGet, "https://api.kindling.systems/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assertSecurityHeaders(t, rec, "direct-tls")
	assertHSTSPresent(t, rec, "direct-tls")
}

// Verify that RequestUsesHTTPS is correctly imported/used
// (we verify by ensuring the logic path from auth package works).
func TestSecurityHeaders_RequestUsesHTTPS_EdgeProxy(t *testing.T) {
	t.Parallel()

	// Verify that auth.RequestUsesHTTPS returns true for X-Forwarded-Proto: https.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if !auth.RequestUsesHTTPS(req) {
		t.Fatal("RequestUsesHTTPS should return true for X-Forwarded-Proto: https")
	}

	// Verify false for plain HTTP.
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	if auth.RequestUsesHTTPS(req2) {
		t.Fatal("RequestUsesHTTPS should return false for plain HTTP")
	}
}
