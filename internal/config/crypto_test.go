package config

import (
	"bytes"
	"strings"
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

func TestProjectSecretEnvelopeRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{'p'}, 32)

	enc, err := EncryptProjectSecretValue(key, "top-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, projectSecretEnvelopePrefix) {
		t.Fatalf("expected encrypted envelope prefix, got %q", enc)
	}

	out, err := DecryptProjectSecretValue(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if out != "top-secret" {
		t.Fatalf("got %q want %q", out, "top-secret")
	}
}

func TestProjectSecretLegacyPlaintextFallback(t *testing.T) {
	key := bytes.Repeat([]byte{'l'}, 32)

	out, err := DecryptProjectSecretValue(key, "legacy-plain")
	if err != nil {
		t.Fatal(err)
	}
	if out != "legacy-plain" {
		t.Fatalf("got %q want %q", out, "legacy-plain")
	}
}

func TestProjectSecretEnvelopeRejectsMalformedCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{'m'}, 32)

	if _, err := DecryptProjectSecretValue(key, projectSecretEnvelopePrefix+"!!!"); err == nil {
		t.Fatal("expected malformed envelope error")
	}
}

func TestProjectSecretEnvelopeSupportsEmptyString(t *testing.T) {
	key := bytes.Repeat([]byte{'e'}, 32)

	enc, err := EncryptProjectSecretValue(key, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecryptProjectSecretValue(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("got %q want empty string", out)
	}
}
