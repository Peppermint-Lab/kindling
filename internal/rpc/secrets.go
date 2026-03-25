package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateWebhookSecret returns a random hex string for GitHub webhook HMAC.
func GenerateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
