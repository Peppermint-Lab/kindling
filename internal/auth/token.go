package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"io"
)

// SessionTokenBytes is the raw cookie value length (before base64 if we encode — we use hex).
const SessionTokenBytes = 32

// NewSessionToken returns a cryptographically random token suitable for a session cookie.
func NewSessionToken() ([]byte, error) {
	b := make([]byte, SessionTokenBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// HashSessionToken returns a fixed-length SHA-256 digest for database lookup.
func HashSessionToken(raw []byte) []byte {
	h := sha256.Sum256(raw)
	return h[:]
}
