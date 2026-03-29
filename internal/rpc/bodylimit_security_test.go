package rpc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kindlingvm/kindling/internal/shared/httputil"
)

const testBodyLimit = 1 << 20 // 1 MiB — matches production maxJSONBodySize

// jsonHandler is a minimal handler that decodes the request JSON body,
// mimicking how real API handlers (login, bootstrap, protected routes) work.
// If decode succeeds it returns 200; on error it returns 400.
func jsonHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			httputil.WriteAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
}

// limitedHandler returns a handler stack with the body limit middleware
// applied in front of the JSON handler.
func limitedHandler() http.Handler {
	return httputil.BodyLimitMiddleware(testBodyLimit, jsonHandler())
}

// assertSingleJSONBody verifies that the recorder's body contains
// exactly one valid JSON object (with no trailing bytes). It returns
// the decoded APIError-shaped struct for further assertions.
func assertSingleJSONBody(t *testing.T, rec *httptest.ResponseRecorder) httputil.APIError {
	t.Helper()

	raw := rec.Body.Bytes()
	if len(raw) == 0 {
		t.Fatal("response body is empty, want a single JSON object")
	}

	// Decode exactly one JSON value.
	dec := json.NewDecoder(bytes.NewReader(raw))
	var errBody httputil.APIError
	if err := dec.Decode(&errBody); err != nil {
		t.Fatalf("could not decode JSON body: %v\nraw body (%d bytes): %s", err, len(raw), string(raw))
	}

	// Ensure there are no extra bytes after the first JSON value.
	trailing, err := io.ReadAll(dec.Buffered())
	if err != nil {
		t.Fatalf("error reading trailing bytes: %v", err)
	}
	// After the buffered data, also check the rest of the reader.
	rest, _ := io.ReadAll(dec.Buffered())
	trailing = append(trailing, rest...)

	// Trim whitespace — a trailing newline from json.Encoder is acceptable.
	if len(bytes.TrimSpace(trailing)) > 0 {
		t.Fatalf("response body contains extra bytes after JSON object (%d trailing bytes)\nfull body: %s",
			len(trailing), string(raw))
	}

	return errBody
}

// ---------- VAL-BODYLIMIT-001: Oversized public JSON bodies rejected with 413 ----------

func TestBodyLimit_OversizedPublicRouteReturns413(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	// Build a JSON body that exceeds 1 MiB.
	oversized := `{"data":"` + strings.Repeat("A", testBodyLimit+1) + `"}`

	// Test with Content-Length set (fast-path).
	t.Run("with_content_length", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(oversized))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (413 Payload Too Large)", rec.Code, http.StatusRequestEntityTooLarge)
		}

		errBody := assertSingleJSONBody(t, rec)
		if errBody.Code != "payload_too_large" {
			t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
		}
		if !strings.Contains(errBody.Error, "too large") {
			t.Fatalf("error message = %q, want substring 'too large'", errBody.Error)
		}
	})

	// Test without Content-Length (chunked — triggers MaxBytesReader path).
	// This is the critical path for the double-write bug: the handler's
	// JSON decode fails, it writes a 400+body, and the middleware must
	// intercept to emit exactly one 413 JSON body with no appended bytes.
	t.Run("without_content_length", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewReader([]byte(oversized))
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1 // force unknown length
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (413 Payload Too Large)", rec.Code, http.StatusRequestEntityTooLarge)
		}

		errBody := assertSingleJSONBody(t, rec)
		if errBody.Code != "payload_too_large" {
			t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
		}
	})
}

// ---------- VAL-BODYLIMIT-002: Oversized protected JSON bodies rejected with 413 ----------

func TestBodyLimit_OversizedProtectedRouteReturns413(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	// Build a JSON body that exceeds 1 MiB, targeting a protected route.
	oversized := `{"name":"` + strings.Repeat("B", testBodyLimit+1) + `"}`

	// With Content-Length (fast path).
	t.Run("with_content_length", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(oversized))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (413 Payload Too Large)", rec.Code, http.StatusRequestEntityTooLarge)
		}

		errBody := assertSingleJSONBody(t, rec)
		if errBody.Code != "payload_too_large" {
			t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
		}
	})

	// Without Content-Length (chunked — exercises the double-write path).
	t.Run("without_content_length", func(t *testing.T) {
		t.Parallel()
		body := bytes.NewReader([]byte(oversized))
		req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d (413 Payload Too Large)", rec.Code, http.StatusRequestEntityTooLarge)
		}

		errBody := assertSingleJSONBody(t, rec)
		if errBody.Code != "payload_too_large" {
			t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
		}
	})
}

// ---------- VAL-BODYLIMIT-003: Normal-sized requests continue to reach handlers ----------

func TestBodyLimit_NormalSizedRequestsWork(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	cases := []struct {
		name string
		path string
		body string
	}{
		{"small_login", "/api/auth/login", `{"email":"user@example.com","password":"secret"}`},
		{"small_bootstrap", "/api/auth/bootstrap", `{"email":"admin@example.com","password":"admin123","display_name":"Admin"}`},
		{"small_protected", "/api/projects", `{"name":"my-project"}`},
		{"empty_json_object", "/api/auth/login", `{}`},
		{"moderate_json", "/api/projects", `{"name":"` + strings.Repeat("x", 1000) + `"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusRequestEntityTooLarge {
				t.Fatalf("normal-sized request was incorrectly rejected with 413")
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
		})
	}
}

// ---------- VAL-BODYLIMIT-004: Exactly-1MB bodies are accepted ----------

func TestBodyLimit_ExactlyOneMBAccepted(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	// Build a JSON body of exactly 1 MiB (1048576 bytes).
	// We need the total body size (including JSON framing) to be exactly 1 MiB.
	// JSON: {"d":"<padding>"} = 8 bytes framing ({"d":""}) + padding
	padding := testBodyLimit - 8 // 8 bytes for {"d":""}
	body := `{"d":"` + strings.Repeat("Z", padding) + `"}`

	if len(body) != testBodyLimit {
		t.Fatalf("test body size = %d, want exactly %d", len(body), testBodyLimit)
	}

	// With Content-Length set.
	t.Run("with_content_length", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Fatalf("exactly 1MB body was rejected with 413 — boundary should be accepted")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	// Without Content-Length (chunked transfer).
	t.Run("without_content_length", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1 // force unknown length
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Fatalf("exactly 1MB body was rejected with 413 — boundary should be accepted")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

// ---------- Boundary: 1 byte over 1MB is rejected ----------

func TestBodyLimit_OneBytePastLimitRejected(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	// 1048577 bytes = 1 MiB + 1 byte.
	padding := testBodyLimit - 8 + 1
	body := `{"d":"` + strings.Repeat("Z", padding) + `"}`

	if len(body) != testBodyLimit+1 {
		t.Fatalf("test body size = %d, want exactly %d", len(body), testBodyLimit+1)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (1 byte past limit)", rec.Code, http.StatusRequestEntityTooLarge)
	}

	errBody := assertSingleJSONBody(t, rec)
	if errBody.Code != "payload_too_large" {
		t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
	}
}

// ---------- JSON error body on 413 ----------

func TestBodyLimit_413HasInformativeJSONBody(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	oversized := `{"data":"` + strings.Repeat("X", testBodyLimit+100) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// Verify Content-Type is JSON.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	// Verify the body is exactly one valid JSON object with no trailing bytes.
	errBody := assertSingleJSONBody(t, rec)
	if errBody.Code != "payload_too_large" {
		t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
	}
	if errBody.Error == "" {
		t.Fatal("error message is empty, want informative message")
	}
	if !strings.Contains(errBody.Error, "1048576") {
		t.Fatalf("error message %q should mention the limit (1048576 bytes)", errBody.Error)
	}
}

// ---------- GET requests bypass body limit (no body) ----------

func TestBodyLimit_GETRequestsUnaffected(t *testing.T) {
	t.Parallel()

	getHandler := httputil.BodyLimitMiddleware(testBodyLimit, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	getHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET request status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ---------- Double-write prevention: single 413 response with no concatenated bodies ----------

// TestBodyLimit_SingleResponseNoDoubleWrite verifies the core fix: when
// MaxBytesReader fires during a handler read and the handler writes its
// own error (e.g., 400 + JSON body), the middleware must emit exactly
// one 413 response body. Before the fix, the handler's error JSON was
// appended after the middleware's 413 JSON, producing concatenated
// invalid JSON.
func TestBodyLimit_SingleResponseNoDoubleWrite(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	// Build a body that is slightly over the limit. With ContentLength
	// unknown, the handler will read the full body, hit MaxBytesError,
	// and try to write its own error response.
	oversized := `{"data":"` + strings.Repeat("D", testBodyLimit+512) + `"}`

	routes := []struct {
		name string
		path string
	}{
		{"public_login", "/api/auth/login"},
		{"public_bootstrap", "/api/auth/bootstrap"},
		{"protected_projects", "/api/projects"},
	}

	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Force chunked path (no Content-Length) to exercise the
			// MaxBytesReader → handler error → middleware intercept flow.
			body := bytes.NewReader([]byte(oversized))
			req := httptest.NewRequest(http.MethodPost, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
			}

			// The critical assertion: response body must be exactly one
			// valid JSON object with no appended/concatenated bytes.
			errBody := assertSingleJSONBody(t, rec)
			if errBody.Code != "payload_too_large" {
				t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
			}
		})
	}
}

// TestBodyLimit_HandlerMultipleWritesDiscarded verifies that even if a
// handler calls Write() multiple times after the body limit fires, all
// writes are silently discarded.
func TestBodyLimit_HandlerMultipleWritesDiscarded(t *testing.T) {
	t.Parallel()

	// A handler that writes multiple chunks to the response body after
	// failing to decode the request.
	multiWriteHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// Write multiple times — all should be discarded by the middleware.
			w.Write([]byte(`{"error":"bad`))
			w.Write([]byte(`_request","code":"invalid_json"}`))
			w.Write([]byte("\nextra trailing data"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := httputil.BodyLimitMiddleware(testBodyLimit, multiWriteHandler)

	oversized := `{"data":"` + strings.Repeat("M", testBodyLimit+1) + `"}`
	body := bytes.NewReader([]byte(oversized))
	req := httptest.NewRequest(http.MethodPost, "/api/test", body)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	errBody := assertSingleJSONBody(t, rec)
	if errBody.Code != "payload_too_large" {
		t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
	}
}

// TestBodyLimit_FastPathSingleResponse verifies that the Content-Length
// fast path also produces exactly one JSON body (no double-write risk
// on this path, but assert for completeness).
func TestBodyLimit_FastPathSingleResponse(t *testing.T) {
	t.Parallel()

	handler := limitedHandler()

	oversized := `{"data":"` + strings.Repeat("F", testBodyLimit+1) + `"}`

	routes := []struct {
		name string
		path string
	}{
		{"public_login", "/api/auth/login"},
		{"protected_projects", "/api/projects"},
	}

	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(oversized))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
			}

			errBody := assertSingleJSONBody(t, rec)
			if errBody.Code != "payload_too_large" {
				t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
			}
		})
	}
}
