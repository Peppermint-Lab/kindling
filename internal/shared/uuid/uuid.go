// Package uuid provides UUID utilities wrapping google/uuid.
package uuid

import "github.com/google/uuid"

// UUID is an alias for google/uuid.UUID.
type UUID = uuid.UUID

// New generates a new random UUID.
func New() UUID { return uuid.New() }

// Parse parses a UUID string.
func Parse(s string) (UUID, error) { return uuid.Parse(s) }

// Nil is the zero-value UUID.
var Nil = uuid.Nil
