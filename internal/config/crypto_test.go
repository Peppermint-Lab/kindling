package config

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptClusterSecretRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{'k'}, 32)
	plain := []byte("secret-token")
	ct, err := EncryptClusterSecret(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecryptClusterSecret(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(plain) {
		t.Fatalf("got %q want %q", out, plain)
	}
}

func TestDecryptClusterSecretEmpty(t *testing.T) {
	key := bytes.Repeat([]byte{'x'}, 32)
	out, err := DecryptClusterSecret(key, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("got %v %q", err, out)
	}
}
