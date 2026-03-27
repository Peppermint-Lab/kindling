package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const projectSecretEnvelopePrefix = "enc:v1:"

// EncryptClusterSecret encrypts plaintext with AES-256-GCM. Output is nonce || ciphertext.
func EncryptClusterSecret(key, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("cluster secret: empty plaintext")
	}
	return encryptSecretBytes(key, plaintext)
}

// DecryptClusterSecret decrypts output from EncryptClusterSecret.
func DecryptClusterSecret(key, ciphertext []byte) ([]byte, error) {
	return decryptSecretBytes(key, ciphertext)
}

// EncryptProjectSecretValue encrypts a project secret into a versioned text envelope.
func EncryptProjectSecretValue(key []byte, plaintext string) (string, error) {
	ct, err := encryptSecretBytes(key, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return projectSecretEnvelopePrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptProjectSecretValue decrypts a project secret envelope.
// Legacy plaintext rows are returned unchanged so old data remains readable until backfilled.
func DecryptProjectSecretValue(key []byte, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !IsEncryptedProjectSecretValue(stored) {
		return stored, nil
	}
	raw := stored[len(projectSecretEnvelopePrefix):]
	if raw == "" {
		return "", errors.New("project secret: missing ciphertext")
	}
	ct, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("project secret: decode envelope: %w", err)
	}
	plain, err := decryptSecretBytes(key, ct)
	if err != nil {
		return "", fmt.Errorf("project secret: decrypt: %w", err)
	}
	return string(plain), nil
}

// IsEncryptedProjectSecretValue reports whether the stored value uses the project secret envelope.
func IsEncryptedProjectSecretValue(stored string) bool {
	return len(stored) >= len(projectSecretEnvelopePrefix) && stored[:len(projectSecretEnvelopePrefix)] == projectSecretEnvelopePrefix
}

func encryptSecretBytes(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cluster secret key: want 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decryptSecretBytes(key, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cluster secret key: want 32 bytes, got %d", len(key))
	}
	if len(ciphertext) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("cluster secret: ciphertext too short")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}
