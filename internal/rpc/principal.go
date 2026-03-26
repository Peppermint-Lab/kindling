package rpc

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/auth"
)

func mustPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, false
	}
	return p, true
}
