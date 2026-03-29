package rpc

import (
	"bytes"
	"encoding/json"
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

		var errBody struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("could not decode error body: %v", err)
		}
		if errBody.Code != "payload_too_large" {
			t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
		}
		if !strings.Contains(errBody.Error, "too large") {
			t.Fatalf("error message = %q, want substring 'too large'", errBody.Error)
		}
	})

	// Test without Content-Length (chunked — triggers MaxBytesReader path).
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

		var errBody struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("could not decode error body: %v", err)
		}
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

	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (413 Payload Too Large)", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// Verify 413 is returned before business logic runs — the handler
	// should never have seen the decoded body since the middleware
	// rejected it.
	var errBody struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("could not decode error body: %v", err)
	}
	if errBody.Code != "payload_too_large" {
		t.Fatalf("error code = %q, want %q", errBody.Code, "payload_too_large")
	}
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

	// Verify JSON structure matches APIError.
	var errBody struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("413 response is not valid JSON: %v", err)
	}
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
