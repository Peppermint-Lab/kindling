package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const WorkerEnrollmentTokenPrefix = "knde_"

const workerEnrollmentSecretBytes = 32

func NewWorkerEnrollmentToken() (plaintext string, tokenHash []byte, err error) {
	b := make([]byte, workerEnrollmentSecretBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", nil, err
	}
	plaintext = WorkerEnrollmentTokenPrefix + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	return plaintext, h[:], nil
}

func HashWorkerEnrollmentToken(plaintext string) []byte {
	h := sha256.Sum256([]byte(plaintext))
	return h[:]
}
