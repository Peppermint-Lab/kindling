package main

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/shared/httputil"
)

// maxJSONBodySize is the maximum allowed request body size for JSON API
// endpoints. Bodies exceeding this limit are rejected with 413 Payload
// Too Large before reaching handler business logic.
const maxJSONBodySize = 1 << 20 // 1 MiB (1048576 bytes)

// bodyLimitMiddleware wraps httputil.BodyLimitMiddleware with the
// configured JSON body size limit.
func bodyLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return httputil.BodyLimitMiddleware(maxBytes, next)
}
