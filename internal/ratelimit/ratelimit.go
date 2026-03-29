// Package ratelimit provides in-memory per-IP rate limiting middleware.
//
// It uses a fixed-window algorithm: each IP gets a bucket that allows up to
// [Limit] requests per [Window]. The bucket resets after the window elapses.
// A background goroutine periodically removes expired entries to prevent
// memory leaks.
package ratelimit

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kindlingvm/kindling/internal/shared/httputil"
)

// bucket tracks request counts within a fixed window for a single IP.
type bucket struct {
	count    int
	windowStart time.Time
}

// Limiter is an in-memory per-IP rate limiter.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
	now     func() time.Time // injectable clock for testing

	stopOnce sync.Once
	stopCh   chan struct{}
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock overrides the time source (useful for testing).
func WithClock(fn func() time.Time) Option {
	return func(l *Limiter) { l.now = fn }
}

// WithCleanupInterval sets how often expired entries are purged.
// A zero or negative value disables automatic cleanup.
func WithCleanupInterval(d time.Duration) Option {
	return func(l *Limiter) {
		if d > 0 {
			go l.cleanupLoop(d)
		}
	}
}

// New creates a Limiter that allows limit requests per window per IP.
// The caller must arrange cleanup (via WithCleanupInterval or NewWithDefaults)
// to prevent unbounded memory growth.
func New(limit int, window time.Duration, opts ...Option) *Limiter {
	l := &Limiter{
		buckets: make(map[string]*bucket),
		limit:   limit,
		window:  window,
		now:     time.Now,
		stopCh:  make(chan struct{}),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// NewWithDefaults creates a Limiter with default cleanup interval (2× window).
func NewWithDefaults(limit int, window time.Duration, opts ...Option) *Limiter {
	l := New(limit, window, opts...)
	// Start default cleanup goroutine.
	go l.cleanupLoop(2 * window)
	return l
}

// Stop halts the cleanup goroutine. Safe to call multiple times.
func (l *Limiter) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

// Allow checks whether a request from the given IP is allowed.
// It returns (allowed bool, remaining int, resetUnix int64).
func (l *Limiter) Allow(ip string) (bool, int, int64) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ip]
	if !ok || now.Sub(b.windowStart) >= l.window {
		// New window.
		l.buckets[ip] = &bucket{count: 1, windowStart: now}
		return true, l.limit - 1, now.Add(l.window).Unix()
	}

	b.count++
	remaining := l.limit - b.count
	resetUnix := b.windowStart.Add(l.window).Unix()

	if b.count > l.limit {
		return false, 0, resetUnix
	}
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining, resetUnix
}

// cleanupLoop removes expired buckets periodically.
func (l *Limiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.stopCh:
			return
		}
	}
}

// cleanup removes all buckets whose window has expired.
func (l *Limiter) cleanup() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	before := len(l.buckets)
	for ip, b := range l.buckets {
		if now.Sub(b.windowStart) >= l.window {
			delete(l.buckets, ip)
		}
	}
	after := len(l.buckets)
	if removed := before - after; removed > 0 {
		slog.Debug("ratelimit: cleaned expired buckets", "removed", removed, "remaining", after)
	}
}

// BucketCount returns the number of tracked IPs (for testing/monitoring).
func (l *Limiter) BucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Middleware returns HTTP middleware that rate-limits requests by client IP.
// It sets X-RateLimit-Limit, X-RateLimit-Remaining, and X-RateLimit-Reset
// headers on every response. When the limit is exceeded it returns 429 with
// a Retry-After header.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		allowed, remaining, resetUnix := l.Allow(ip)

		// Always set rate limit headers.
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))

		if !allowed {
			retryAfter := resetUnix - l.now().Unix()
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			httputil.WriteAPIError(w, http.StatusTooManyRequests,
				"rate_limit_exceeded",
				fmt.Sprintf("rate limit exceeded; try again in %d seconds", retryAfter))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// PathMiddleware returns HTTP middleware that rate-limits only requests matching
// the given method+path combinations. Other requests pass through unmodified.
func (l *Limiter) PathMiddleware(targets map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if !targets[key] {
			next.ServeHTTP(w, r)
			return
		}
		l.Middleware(next).ServeHTTP(w, r)
	})
}

// extractIP extracts the client IP from r.RemoteAddr, stripping port if present.
func extractIP(r *http.Request) string {
	addr := r.RemoteAddr
	if addr == "" {
		return ""
	}
	// Handle host:port
	if strings.Contains(addr, ":") {
		host, _, err := net.SplitHostPort(addr)
		if err == nil {
			return host
		}
	}
	return addr
}
