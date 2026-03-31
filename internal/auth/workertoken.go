package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const WorkerAgentTokenPrefix = "kndw_"

const WorkerAgentTokenSecretBytes = 32

func NewWorkerAgentToken() (plaintext string, tokenHash []byte, err error) {
	b := make([]byte, WorkerAgentTokenSecretBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", nil, err
	}
	plaintext = WorkerAgentTokenPrefix + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

func HashWorkerAgentToken(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}
