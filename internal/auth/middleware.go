package auth

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized", "code": "unauthorized"})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message, "code": code})
}

// Middleware enforces session cookies on API routes except PublicRoute.
func Middleware(q *queries.Queries, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			writeUnauthorized(w)
			return
		}
		raw, err := hex.DecodeString(strings.TrimSpace(cookie.Value))
		if err != nil || len(raw) != SessionTokenBytes {
			writeUnauthorized(w)
			return
		}

		sess, err := q.UserSessionByTokenHash(r.Context(), HashSessionToken(raw))
		if err != nil {
			if err == pgx.ErrNoRows {
				writeUnauthorized(w)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, "internal", "session lookup failed")
			return
		}

		u, err := q.UserByID(r.Context(), sess.UserID)
		if err != nil {
			writeUnauthorized(w)
			return
		}

		mem, err := q.OrganizationMembershipByUserAndOrg(r.Context(), queries.OrganizationMembershipByUserAndOrgParams{
			UserID:         sess.UserID,
			OrganizationID: sess.CurrentOrganizationID,
		})
		if err != nil {
			writeUnauthorized(w)
			return
		}

		p := Principal{
			UserID:         uuid.UUID(u.ID.Bytes),
			Email:          u.Email,
			PlatformAdmin:  u.IsPlatformAdmin,
			OrgID:          uuid.UUID(sess.CurrentOrganizationID.Bytes),
			OrgRole:        mem.Role,
			SessionID:      uuid.UUID(sess.ID.Bytes),
			OrganizationID: sess.CurrentOrganizationID,
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// PublicRoute reports routes that skip session enforcement.
func PublicRoute(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	path := r.URL.Path
	switch path {
	case "/healthz":
		return r.Method == http.MethodGet
	case "/":
		return r.Method == http.MethodGet
	}
	if strings.HasPrefix(path, "/webhooks/") {
		return true
	}
	if path == "/api/auth/bootstrap" && r.Method == http.MethodPost {
		return true
	}
	if path == "/api/auth/login" && r.Method == http.MethodPost {
		return true
	}
	if path == "/api/auth/logout" && r.Method == http.MethodPost {
		return true
	}
	if path == "/api/auth/session" && r.Method == http.MethodGet {
		return true
	}
	if path == "/api/auth/bootstrap-status" && r.Method == http.MethodGet {
		return true
	}
	if path == "/api/auth/providers" && r.Method == http.MethodGet {
		return true
	}
	if strings.HasPrefix(path, "/api/auth/providers/") && r.Method == http.MethodGet {
		if strings.HasSuffix(path, "/start") || strings.HasSuffix(path, "/callback") {
			return true
		}
	}
	return false
}

// SetSessionCookie sets the HttpOnly session cookie (raw token must be SessionTokenBytes long, hex-encoded for transport).
func SetSessionCookie(w http.ResponseWriter, r *http.Request, rawToken []byte, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    hex.EncodeToString(rawToken),
		Path:     "/",
		MaxAge:   SessionMaxAgeSeconds,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ClearSessionCookie clears the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// SessionDBExpiry returns the session expiry timestamp stored in the database.
func SessionDBExpiry() time.Time {
	return time.Now().UTC().Add(30 * 24 * time.Hour)
}

// RequestUsesHTTPS is a conservative signal for the Secure cookie flag when TLS terminates locally.
func RequestUsesHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// PgUUID wraps a uuid.UUID for sqlc pgtype fields.
func PgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
