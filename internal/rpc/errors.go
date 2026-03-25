package rpc

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiError is the standard JSON error envelope for the dashboard API.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
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
	_ = json.NewEncoder(w).Encode(apiError{Error: message, Code: code})
}

func writeAPIErrorFromErr(w http.ResponseWriter, status int, code string, err error) {
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
	_ = json.NewEncoder(w).Encode(apiError{Error: msg, Code: code})
}
