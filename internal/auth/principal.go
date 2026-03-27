package auth

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type ctxKey int

const principalKey ctxKey = iota

// Principal is an authenticated dashboard/control-plane user in a chosen org.
type Principal struct {
	UserID         uuid.UUID
	Email          string
	PlatformAdmin  bool
	OrgID          uuid.UUID
	OrgRole        string
	SessionID      uuid.UUID
	// APIKeyID is non-zero when the request was authenticated with an API key
	// (organization is fixed for that key; switch-org is not supported).
	APIKeyID uuid.UUID
	OrganizationID pgtype.UUID // same as OrgID in pgtype form for sqlc calls
}

// WithPrincipal stores the principal in the request context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFrom returns the principal and whether the request is authenticated.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
