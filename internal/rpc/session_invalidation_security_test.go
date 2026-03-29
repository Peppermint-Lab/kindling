package rpc

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kindlingvm/kindling/internal/auth"
)

// TestSessionTokenHashFromRequest verifies that the session token hash
// extraction helper correctly parses the session cookie and returns
// the SHA-256 hash used for database lookup.
func TestSessionTokenHashFromRequest(t *testing.T) {
	t.Parallel()

	// Generate a known token to verify the hash matches.
	rawToken, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	expectedHash := auth.HashSessionToken(rawToken)

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: hex.EncodeToString(rawToken),
	})

	got := sessionTokenHashFromRequest(req)
	if got == nil {
		t.Fatal("sessionTokenHashFromRequest returned nil, want token hash")
	}
	if len(got) != len(expectedHash) {
		t.Fatalf("hash length = %d, want %d", len(got), len(expectedHash))
	}
	for i := range got {
		if got[i] != expectedHash[i] {
			t.Fatalf("hash mismatch at byte %d: got %02x, want %02x", i, got[i], expectedHash[i])
		}
	}
}

// TestSessionTokenHashFromRequest_MissingCookie verifies that the helper
// returns nil when no session cookie is present.
func TestSessionTokenHashFromRequest_MissingCookie(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	if got := sessionTokenHashFromRequest(req); got != nil {
		t.Fatalf("expected nil for missing cookie, got %x", got)
	}
}

// TestSessionTokenHashFromRequest_EmptyCookie verifies that an empty cookie
// value returns nil.
func TestSessionTokenHashFromRequest_EmptyCookie(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "",
	})
	if got := sessionTokenHashFromRequest(req); got != nil {
		t.Fatalf("expected nil for empty cookie, got %x", got)
	}
}

// TestSessionTokenHashFromRequest_InvalidHex verifies that a cookie with
// invalid hex returns nil.
func TestSessionTokenHashFromRequest_InvalidHex(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "not-valid-hex-string!@#$",
	})
	if got := sessionTokenHashFromRequest(req); got != nil {
		t.Fatalf("expected nil for invalid hex cookie, got %x", got)
	}
}

// TestSessionTokenHashFromRequest_WrongLength verifies that a cookie with
// valid hex but wrong length returns nil.
func TestSessionTokenHashFromRequest_WrongLength(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	req.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: hex.EncodeToString([]byte("too-short")),
	})
	if got := sessionTokenHashFromRequest(req); got != nil {
		t.Fatalf("expected nil for wrong-length cookie, got %x", got)
	}
}

// TestPasswordChangeRouteRequiresAuth verifies that PUT /api/auth/password
// is NOT a public route (requires session/authentication).
func TestPasswordChangeRouteRequiresAuth(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	if auth.PublicRoute(req) {
		t.Fatal("expected PUT /api/auth/password to require authentication (not be a public route)")
	}
}

// TestPasswordChangeHandlerRejectsUnauthenticated verifies that the password
// change handler returns 401 when no principal is set in context (i.e.,
// no session or API key authentication).
func TestPasswordChangeHandlerRejectsUnauthenticated(t *testing.T) {
	t.Parallel()

	api := &API{}
	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)
	rec := httptest.NewRecorder()

	api.authChangePassword(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestSessionTokenHashDeterministic verifies that the same raw token
// always produces the same hash (deterministic SHA-256).
func TestSessionTokenHashDeterministic(t *testing.T) {
	t.Parallel()

	rawToken, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}

	hash1 := auth.HashSessionToken(rawToken)
	hash2 := auth.HashSessionToken(rawToken)

	if len(hash1) != len(hash2) {
		t.Fatalf("hash lengths differ: %d vs %d", len(hash1), len(hash2))
	}
	for i := range hash1 {
		if hash1[i] != hash2[i] {
			t.Fatalf("hash mismatch at byte %d", i)
		}
	}
}

// TestDifferentTokensDifferentHashes verifies that different tokens produce
// different hashes (collision resistance for session identification).
func TestDifferentTokensDifferentHashes(t *testing.T) {
	t.Parallel()

	tok1, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken 1: %v", err)
	}
	tok2, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken 2: %v", err)
	}

	hash1 := auth.HashSessionToken(tok1)
	hash2 := auth.HashSessionToken(tok2)

	same := true
	for i := range hash1 {
		if hash1[i] != hash2[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("two different tokens produced the same hash — expected different hashes")
	}
}

// TestPasswordChangeHandlerRejectsMissingBody verifies that the handler
// returns 400 when the request has an authenticated principal but no
// JSON body.
func TestPasswordChangeHandlerRejectsMissingBody(t *testing.T) {
	t.Parallel()

	api := &API{}
	req := httptest.NewRequest(http.MethodPut, "/api/auth/password", nil)

	// Inject a principal to simulate an authenticated request.
	p := auth.Principal{
		UserID:    [16]byte{1},
		SessionID: [16]byte{2},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))

	rec := httptest.NewRecorder()
	api.authChangePassword(rec, req)

	// nil body causes json.Decode to return io.EOF → invalid_json
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestPasswordChangeHandlerRejectsEmptyPasswords verifies that the handler
// returns 400 when current_password or new_password are empty.
func TestPasswordChangeHandlerRejectsEmptyPasswords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"empty current_password", `{"current_password":"","new_password":"newpass123"}`},
		{"empty new_password", `{"current_password":"oldpass123","new_password":""}`},
		{"both empty", `{"current_password":"","new_password":""}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := &API{}
			req := httptest.NewRequest(http.MethodPut, "/api/auth/password",
				strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")

			p := auth.Principal{
				UserID:    [16]byte{1},
				SessionID: [16]byte{2},
			}
			req = req.WithContext(auth.WithPrincipal(req.Context(), p))

			rec := httptest.NewRecorder()
			api.authChangePassword(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

// TestPasswordChangeHandlerRejectsShortNewPassword verifies that the handler
// enforces a minimum password length of 8 characters.
func TestPasswordChangeHandlerRejectsShortNewPassword(t *testing.T) {
	t.Parallel()

	api := &API{}
	body := `{"current_password":"oldpassword123","new_password":"short"}`
	req := httptest.NewRequest(http.MethodPut, "/api/auth/password",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	p := auth.Principal{
		UserID:    [16]byte{1},
		SessionID: [16]byte{2},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))

	rec := httptest.NewRecorder()
	api.authChangePassword(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
