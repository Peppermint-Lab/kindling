package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Identity is a normalized external identity payload returned by an auth provider.
type Identity struct {
	Subject     string
	Email       string
	Login       string
	DisplayName string
	Claims      map[string]any
}

// RandomToken returns a URL-safe random token with the requested number of random bytes.
func RandomToken(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PKCEChallenge returns the S256 challenge for a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SplitScopes normalizes a whitespace-delimited scope string.
func SplitScopes(raw string, defaults []string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		fields = append([]string(nil), defaults...)
	}
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}
