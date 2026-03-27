// Package pguuid provides helpers for converting between google/uuid and pgtype.UUID.
package pguuid

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// ToPgtype converts a uuid.UUID to a pgtype.UUID with Valid=true.
func ToPgtype(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// FromPgtype converts a pgtype.UUID back to a uuid.UUID.
// Returns uuid.Nil when the pgtype value is not valid.
func FromPgtype(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return u.Bytes
}

// ToString returns the string representation of a pgtype.UUID.
// Returns an empty string when the value is not valid.
func ToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// Equal reports whether two pgtype.UUID values are equal.
func Equal(a, b pgtype.UUID) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Bytes == b.Bytes)
}
