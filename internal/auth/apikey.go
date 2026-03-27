package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// APIKeyPrefix is the prefix for plaintext API key tokens stored by clients.
const APIKeyPrefix = "knd_"

// APIKeySecretBytes is the random portion length (hex-encoded after the prefix).
const APIKeySecretBytes = 32

// NewAPIKeyToken returns a new API key string (e.g. knd_ + hex) and its SHA-256 hash for storage.
func NewAPIKeyToken() (plaintext string, tokenHash []byte, err error) {
	b := make([]byte, APIKeySecretBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", nil, err
	}
	plaintext = APIKeyPrefix + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

// HashAPIKeyToken returns SHA-256 of the full plaintext token (including prefix).
func HashAPIKeyToken(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}
