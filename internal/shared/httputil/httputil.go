// Package httputil provides shared HTTP helpers for the Kindling API.
package httputil

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// APIError is the standard JSON error envelope for the dashboard API.
type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// WriteAPIError writes a JSON error response with optional logging.
// 5xx errors are logged at ERROR level; 4xx at WARN level.
func WriteAPIError(w http.ResponseWriter, status int, code, message string) {
	if message == "" {
		message = http.StatusText(status)
	}
	if code == "" {
		code = http.StatusText(status)
	}
	if status >= 500 {
		slog.Error("API error", "status", status, "code", code, "message", message)
	} else {
		slog.Warn("API validation", "status", status, "code", code, "message", message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: message, Code: code})
}

// WriteAPIErrorFromErr writes a JSON error response derived from an error value.
func WriteAPIErrorFromErr(w http.ResponseWriter, status int, code string, err error) {
	msg := err.Error()
	if code == "" {
		code = "internal_error"
	}
	if status >= 500 {
		slog.Error("API server error", "status", status, "code", code, "error", err)
	} else {
		slog.Warn("API client error", "status", status, "code", code, "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: msg, Code: code})
}
