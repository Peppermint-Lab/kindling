package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// dummyHandler returns 200 OK for any request.
func dummyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})
}

// testClock provides a controllable time source for tests.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(t time.Time) *testClock { return &testClock{now: t} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---------- VAL-RATELIMIT-001 + VAL-RATELIMIT-002 ----------

func TestLoginRateLimit_10PassThen11thBlocked(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	clientIP := "192.0.2.10:54321"

	// Attempts 1–10 must NOT return 429.
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = clientIP
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: got 429 before limit reached", i)
		}

		// Verify rate limit headers are present.
		limitH := rec.Header().Get("X-RateLimit-Limit")
		if limitH != "10" {
			t.Fatalf("attempt %d: X-RateLimit-Limit = %q, want %q", i, limitH, "10")
		}
		remainingH := rec.Header().Get("X-RateLimit-Remaining")
		wantRemaining := strconv.Itoa(10 - i)
		if remainingH != wantRemaining {
			t.Fatalf("attempt %d: X-RateLimit-Remaining = %q, want %q", i, remainingH, wantRemaining)
		}
		resetH := rec.Header().Get("X-RateLimit-Reset")
		if resetH == "" {
			t.Fatalf("attempt %d: X-RateLimit-Reset header missing", i)
		}
	}

	// Attempt 11 must return 429.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = clientIP
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 11: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	// Verify Retry-After header present.
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("attempt 11: Retry-After header missing")
	}
	retryVal, err := strconv.Atoi(retryAfter)
	if err != nil || retryVal <= 0 {
		t.Fatalf("attempt 11: Retry-After = %q, want positive integer", retryAfter)
	}

	// Verify rate limit headers on blocked response.
	if rec.Header().Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("attempt 11: X-RateLimit-Limit = %q, want %q", rec.Header().Get("X-RateLimit-Limit"), "10")
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("attempt 11: X-RateLimit-Remaining = %q, want %q", rec.Header().Get("X-RateLimit-Remaining"), "0")
	}

	// Verify JSON error body identifies rate limiting.
	var errBody struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("attempt 11: could not decode error body: %v", err)
	}
	if errBody.Code != "rate_limit_exceeded" {
		t.Fatalf("attempt 11: error code = %q, want %q", errBody.Code, "rate_limit_exceeded")
	}
}

// ---------- VAL-RATELIMIT-003 ----------

func TestRateLimitRecoveryAfterWindow(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	clientIP := "192.0.2.20:54321"

	// Exhaust the limiter.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = clientIP
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	// Confirm blocked.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = clientIP
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("pre-recovery: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	// Advance past the 60-second window.
	clock.Advance(61 * time.Second)

	// Request should now succeed with a refreshed budget.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = clientIP
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("post-recovery: still blocked after window expired")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("post-recovery: status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Headers should show refreshed budget.
	remaining := rec.Header().Get("X-RateLimit-Remaining")
	if remaining != "9" {
		t.Fatalf("post-recovery: X-RateLimit-Remaining = %q, want %q", remaining, "9")
	}
}

// ---------- VAL-RATELIMIT-004 ----------

func TestBootstrapSameRateLimit(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	clientIP := "192.0.2.30:54321"

	// 10 requests to bootstrap should pass.
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
		req.RemoteAddr = clientIP
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("bootstrap attempt %d: got 429 before limit reached", i)
		}

		limitH := rec.Header().Get("X-RateLimit-Limit")
		if limitH != "10" {
			t.Fatalf("bootstrap attempt %d: X-RateLimit-Limit = %q, want %q", i, limitH, "10")
		}
	}

	// 11th should be blocked.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
	req.RemoteAddr = clientIP
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("bootstrap attempt 11: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("bootstrap attempt 11: Retry-After header missing")
	}

	// Recovery after window.
	clock.Advance(61 * time.Second)
	req = httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap", nil)
	req.RemoteAddr = clientIP
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("bootstrap post-recovery: still blocked after window expired")
	}
}

// ---------- VAL-RATELIMIT-005 ----------

func TestBucketIsolation(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	ipA := "192.0.2.100:54321"
	ipB := "198.51.100.50:54321"

	// Exhaust IP A's budget.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = ipA
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	// IP A should be blocked.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = ipA
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP A: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	// IP B should have its own budget — all 10 requests should pass.
	for i := 1; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = ipB
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("IP B attempt %d: got 429 — budget was not isolated", i)
		}
	}

	// IP B should now also be blocked.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = ipB
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP B attempt 11: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

// ---------- Header correctness ----------

func TestRateLimitHeaders(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	clientIP := "192.0.2.40:54321"

	// First request: check all headers.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = clientIP
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-RateLimit-Limit") != "10" {
		t.Fatalf("X-RateLimit-Limit = %q, want %q", rec.Header().Get("X-RateLimit-Limit"), "10")
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Fatalf("X-RateLimit-Remaining = %q, want %q", rec.Header().Get("X-RateLimit-Remaining"), "9")
	}
	resetStr := rec.Header().Get("X-RateLimit-Reset")
	resetVal, err := strconv.ParseInt(resetStr, 10, 64)
	if err != nil {
		t.Fatalf("X-RateLimit-Reset = %q, not a valid unix timestamp", resetStr)
	}
	expectedReset := clock.Now().Add(60 * time.Second).Unix()
	if resetVal != expectedReset {
		t.Fatalf("X-RateLimit-Reset = %d, want %d", resetVal, expectedReset)
	}

	// Monotonically decreasing remaining.
	prevRemaining := 9
	for i := 2; i <= 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = clientIP
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		remaining, err := strconv.Atoi(rec.Header().Get("X-RateLimit-Remaining"))
		if err != nil {
			t.Fatalf("attempt %d: could not parse X-RateLimit-Remaining: %v", i, err)
		}
		if remaining >= prevRemaining {
			t.Fatalf("attempt %d: remaining %d is not less than previous %d", i, remaining, prevRemaining)
		}
		prevRemaining = remaining
	}

	// 429 response also has all headers.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = clientIP
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"} {
		if rec.Header().Get(h) == "" {
			t.Fatalf("header %q missing on 429 response", h)
		}
	}
}

// ---------- Cleanup removes expired entries ----------

func TestCleanupRemovesExpiredEntries(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	// Consume a request from two IPs.
	limiter.Allow("192.0.2.1")
	limiter.Allow("192.0.2.2")

	if limiter.BucketCount() != 2 {
		t.Fatalf("buckets = %d, want 2", limiter.BucketCount())
	}

	// Advance past window.
	clock.Advance(61 * time.Second)

	// Run cleanup explicitly.
	limiter.cleanup()

	if limiter.BucketCount() != 0 {
		t.Fatalf("after cleanup: buckets = %d, want 0", limiter.BucketCount())
	}
}

// ---------- extractIP ----------

func TestExtractIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"ipv4_with_port", "192.0.2.1:1234", "192.0.2.1"},
		{"ipv4_no_port", "192.0.2.1", "192.0.2.1"},
		{"ipv6_with_port", "[::1]:1234", "::1"},
		{"ipv6_no_port", "::1", "::1"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			got := extractIP(req)
			if got != tc.want {
				t.Fatalf("extractIP(%q) = %q, want %q", tc.remoteAddr, got, tc.want)
			}
		})
	}
}

// ---------- Goroutine safety ----------

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	clock := newTestClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	limiter := New(10, 60*time.Second, WithClock(clock.Now))
	defer limiter.Stop()

	handler := limiter.Middleware(dummyHandler())
	var wg sync.WaitGroup

	// 20 goroutines each making 5 requests from different IPs.
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ip := "10.0.0." + strconv.Itoa(id) + ":1234"
			for j := 0; j < 5; j++ {
				req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
				req.RemoteAddr = ip
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				// All should pass — under the limit.
				if rec.Code == http.StatusTooManyRequests {
					t.Errorf("goroutine %d, request %d: unexpectedly rate limited", id, j)
				}
			}
		}(g)
	}
	wg.Wait()
}
