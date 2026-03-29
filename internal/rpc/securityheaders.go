package rpc

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/auth"
)

// SecurityHeadersMiddleware sets standard security response headers on every
// response. It should be applied early in the middleware chain so that ALL
// responses — including 401, 403, 413, 429 errors — include these headers.
//
// Headers set:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Strict-Transport-Security: max-age=63072000; includeSubDomains (HTTPS only)
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// HSTS only on HTTPS connections (direct TLS or via X-Forwarded-Proto).
		if auth.RequestUsesHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}
