package rpc

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/httputil"
)

// writeAPIError delegates to the shared httputil package.
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	httputil.WriteAPIError(w, status, code, message)
}

// writeAPIErrorFromErr delegates to the shared httputil package.
func writeAPIErrorFromErr(w http.ResponseWriter, status int, code string, err error) {
	httputil.WriteAPIErrorFromErr(w, status, code, err)
}

func isPgUniqueViolation(err error) bool { return rpcutil.IsPgUniqueViolation(err) }
