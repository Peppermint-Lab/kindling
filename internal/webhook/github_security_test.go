package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// computeHMAC returns the hex-encoded HMAC-SHA256 of body using the given key.
func computeHMAC(t *testing.T, key, body string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestEnforceSignature_BlankSecretRejectsAll verifies that a project with an
// empty webhook secret rejects ALL deliveries, even when no signature header
// is sent. (VAL-WEBHOOK-001)
func TestEnforceSignature_BlankSecretRejectsAll(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)

	tests := []struct {
		name   string
		secret string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"tab", "\t"},
		{"newline", "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			ok := enforceSignature(w, body, "", tt.secret)
			if ok {
				t.Fatalf("enforceSignature should return false for blank secret %q", tt.secret)
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for blank secret, got %d", w.Code)
			}
			if !strings.Contains(w.Body.String(), "webhook secret not configured") {
				t.Fatalf("expected error message about unconfigured secret, got %q", w.Body.String())
			}
		})
	}
}

// TestEnforceSignature_BlankSecretRejectsEmptyKeyHMAC verifies that even an
// HMAC computed with an empty key is rejected when the project has a blank
// secret. Prevents an attacker from computing HMAC("", body). (VAL-WEBHOOK-005)
func TestEnforceSignature_BlankSecretRejectsEmptyKeyHMAC(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)
	// Compute a valid HMAC using empty string as the key.
	emptyKeyHMAC := "sha256=" + computeHMAC(t, "", string(body))

	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, emptyKeyHMAC, "")
	if ok {
		t.Fatalf("enforceSignature must reject HMAC computed with empty key when secret is blank")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for blank secret with empty-key HMAC, got %d", w.Code)
	}
}

// TestEnforceSignature_MissingSignatureHeader verifies that a missing
// X-Hub-Signature-256 header returns 401. (VAL-WEBHOOK-002 partial)
func TestEnforceSignature_MissingSignatureHeader(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)
	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, "", "my-secret")
	if ok {
		t.Fatalf("enforceSignature should reject missing signature")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing X-Hub-Signature-256") {
		t.Fatalf("expected message about missing signature, got %q", w.Body.String())
	}
}

// TestEnforceSignature_MalformedSignatures verifies that signatures with wrong
// prefix or non-hex digest return 401. (VAL-WEBHOOK-002)
func TestEnforceSignature_MalformedSignatures(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "my-webhook-secret"

	tests := []struct {
		name      string
		signature string
		errSubstr string
	}{
		{"sha1 prefix", "sha1=abcdef1234567890abcdef1234567890abcdef12", "invalid signature prefix"},
		{"no prefix", "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab", "invalid signature prefix"},
		{"md5 prefix", "md5=abcdef1234567890abcdef1234567890", "invalid signature prefix"},
		{"sha256 with non-hex", "sha256=zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "malformed signature hex"},
		{"sha256 with odd-length hex", "sha256=abc", "malformed signature hex"}, // odd-length hex is invalid
		{"sha256 with short even hex", "sha256=abcd", "signature mismatch"}, // valid hex but wrong HMAC — hits 403
		{"empty after prefix", "sha256=", "signature mismatch"},             // empty hex decodes to empty bytes, wrong HMAC
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			ok := enforceSignature(w, body, tt.signature, secret)
			if ok {
				t.Fatalf("enforceSignature should reject malformed signature %q", tt.signature)
			}
			// All malformed cases should return 401 or 403 (never 200).
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("expected 401 or 403, got %d for signature %q", w.Code, tt.signature)
			}
			if !strings.Contains(w.Body.String(), tt.errSubstr) {
				t.Fatalf("expected error containing %q, got %q", tt.errSubstr, w.Body.String())
			}
		})
	}
}

// TestEnforceSignature_WrongPrefixReturns401 specifically ensures non-sha256
// prefixes get 401 (not 403). (VAL-WEBHOOK-002)
func TestEnforceSignature_WrongPrefixReturns401(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "my-secret"
	validHex := computeHMAC(t, secret, string(body))

	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, "sha1="+validHex, secret)
	if ok {
		t.Fatal("should reject sha1 prefix")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong prefix should return 401, got %d", w.Code)
	}
}

// TestEnforceSignature_NonHexReturns401 ensures non-hex characters get 401.
// (VAL-WEBHOOK-002)
func TestEnforceSignature_NonHexReturns401(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main"}`)
	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, "sha256=not-valid-hex-chars!@#$%^&*()", "my-secret")
	if ok {
		t.Fatal("should reject non-hex")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-hex should return 401, got %d", w.Code)
	}
}

// TestEnforceSignature_TamperedHMACReturns403 verifies that a correctly
// formatted but wrong HMAC returns 403. (VAL-WEBHOOK-003)
func TestEnforceSignature_TamperedHMACReturns403(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main","after":"abc123"}`)
	secret := "real-secret"

	// Compute HMAC with wrong key.
	wrongKeyHMAC := "sha256=" + computeHMAC(t, "wrong-secret", string(body))

	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, wrongKeyHMAC, secret)
	if ok {
		t.Fatalf("enforceSignature should reject tampered HMAC")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for tampered HMAC, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "signature mismatch") {
		t.Fatalf("expected mismatch message, got %q", w.Body.String())
	}
}

// TestEnforceSignature_TamperedBodyReturns403 verifies that modifying the
// payload body after signing returns 403. (VAL-WEBHOOK-003)
func TestEnforceSignature_TamperedBodyReturns403(t *testing.T) {
	t.Parallel()

	originalBody := `{"ref":"refs/heads/main"}`
	secret := "my-secret"
	validSig := "sha256=" + computeHMAC(t, secret, originalBody)

	// Send the valid signature but with a tampered body.
	tamperedBody := []byte(`{"ref":"refs/heads/evil"}`)
	w := httptest.NewRecorder()
	ok := enforceSignature(w, tamperedBody, validSig, secret)
	if ok {
		t.Fatal("should reject signature for tampered body")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for tampered body, got %d", w.Code)
	}
}

// TestEnforceSignature_ValidHMACAccepted verifies that a correctly signed
// webhook passes validation. (VAL-WEBHOOK-004)
func TestEnforceSignature_ValidHMACAccepted(t *testing.T) {
	t.Parallel()

	body := []byte(`{"ref":"refs/heads/main","after":"abc123"}`)
	secret := "my-webhook-secret"
	validSig := "sha256=" + computeHMAC(t, secret, string(body))

	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, validSig, secret)
	if !ok {
		t.Fatalf("enforceSignature should accept valid HMAC, response: %d %s", w.Code, w.Body.String())
	}
	// On success the recorder should not have a status written.
	if w.Code != http.StatusOK {
		t.Fatalf("expected default 200 (no write), got %d", w.Code)
	}
}

// TestEnforceSignature_ValidHMACWithVariousPayloads checks that signature
// enforcement works for different event payloads — ensuring enforcement is
// applied before event dispatch for all event types. (VAL-WEBHOOK-004)
func TestEnforceSignature_ValidHMACWithVariousPayloads(t *testing.T) {
	t.Parallel()

	secret := "shared-secret-123"
	payloads := []struct {
		name string
		body string
	}{
		{"push event", `{"ref":"refs/heads/main","after":"deadbeef","repository":{"full_name":"org/repo"}}`},
		{"pull_request event", `{"action":"opened","number":42,"pull_request":{"head":{"ref":"feature","sha":"abc"}},"repository":{"full_name":"org/repo"}}`},
		{"workflow_job event", `{"action":"queued","repository":{"full_name":"org/repo"},"workflow_job":{"id":1}}`},
		{"empty body", ``},
		{"large body", strings.Repeat("x", 65536)},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sig := "sha256=" + computeHMAC(t, secret, tt.body)
			w := httptest.NewRecorder()
			ok := enforceSignature(w, []byte(tt.body), sig, secret)
			if !ok {
				t.Fatalf("valid HMAC should be accepted for %s payload, got %d: %s", tt.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestEnforceSignature_SignatureEnforcedBeforeDispatch ensures the signature
// check happens BEFORE any event-specific business logic. We verify this by
// confirming that enforceSignature does not need to know the event type.
func TestEnforceSignature_SignatureEnforcedBeforeDispatch(t *testing.T) {
	t.Parallel()

	// For all event types, the same enforceSignature call is used.
	// If the signature is invalid, the handler returns before dispatch.
	body := []byte(`{"action":"queued"}`)
	secret := "test-secret"
	wrongSig := "sha256=" + computeHMAC(t, "attacker-key", string(body))

	w := httptest.NewRecorder()
	ok := enforceSignature(w, body, wrongSig, secret)
	if ok {
		t.Fatal("wrong signature should be rejected regardless of event type")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

// TestVerifySignature_LegacyFunction tests the original verifySignature
// function still works correctly.
func TestVerifySignature_LegacyFunction(t *testing.T) {
	t.Parallel()

	body := []byte(`test payload`)
	secret := "legacy-secret"
	validSig := "sha256=" + computeHMAC(t, secret, string(body))

	if !verifySignature(body, validSig, secret) {
		t.Fatal("verifySignature should accept valid HMAC")
	}
	if verifySignature(body, "sha256=0000000000000000000000000000000000000000000000000000000000000000", secret) {
		t.Fatal("verifySignature should reject wrong HMAC")
	}
	if verifySignature(body, "sha1="+computeHMAC(t, secret, string(body)), secret) {
		t.Fatal("verifySignature should reject wrong prefix")
	}
	if verifySignature(body, "", secret) {
		t.Fatal("verifySignature should reject empty signature")
	}
}
