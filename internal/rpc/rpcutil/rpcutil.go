// Package rpcutil provides shared utilities for RPC handler sub-packages.
package rpcutil

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/auth"
	"github.com/kindlingvm/kindling/internal/shared/httputil"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

// WriteAPIError delegates to the shared httputil package.
func WriteAPIError(w http.ResponseWriter, status int, code, message string) {
	httputil.WriteAPIError(w, status, code, message)
}

// WriteAPIErrorFromErr delegates to the shared httputil package.
func WriteAPIErrorFromErr(w http.ResponseWriter, status int, code string, err error) {
	httputil.WriteAPIErrorFromErr(w, status, code, err)
}

// WriteJSON serializes data to JSON and writes it with the given status code.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// ParseUUID parses a string into a pgtype.UUID.
func ParseUUID(s string) (pgtype.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

// MustPrincipal extracts the auth principal from the request, writing an
// error response if not present.
func MustPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		WriteAPIError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, false
	}
	return p, true
}

// OrgRoleCanManage returns true if the role has management permissions.
func OrgRoleCanManage(role string) bool {
	return role == "owner" || role == "admin"
}

// RequireOrgAdmin checks that the principal has owner or admin role.
func RequireOrgAdmin(w http.ResponseWriter, p auth.Principal) bool {
	if OrgRoleCanManage(p.OrgRole) {
		return true
	}
	WriteAPIError(w, http.StatusForbidden, "forbidden", "owner or admin role required")
	return false
}

// RequirePlatformAdmin checks that the principal has platform admin privileges.
func RequirePlatformAdmin(w http.ResponseWriter, p auth.Principal) bool {
	if p.PlatformAdmin {
		return true
	}
	WriteAPIError(w, http.StatusForbidden, "forbidden", "platform admin required")
	return false
}

// IsPgUniqueViolation checks if an error is a Postgres unique constraint violation.
func IsPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// PgTextString returns the string value of a pgtype.Text, or empty string if invalid.
func PgTextString(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// FormatTS formats a pgtype.Timestamptz to an RFC3339Nano string pointer, or nil if invalid.
func FormatTS(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

// OptionalUUIDString returns a pointer to the UUID string, or nil if invalid.
func OptionalUUIDString(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := pguuid.ToString(u)
	return &s
}
