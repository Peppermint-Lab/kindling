package sshtrust

import "testing"

func TestNormalizeAuthorizedKey(t *testing.T) {
	t.Parallel()

	const input = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e sandbox@example\n"
	got, err := NormalizeAuthorizedKey(input)
	if err != nil {
		t.Fatalf("NormalizeAuthorizedKey: %v", err)
	}
	const want = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e"
	if got != want {
		t.Fatalf("normalized key = %q, want %q", got, want)
	}
}

func TestExtractMarkedAuthorizedKey(t *testing.T) {
	t.Parallel()

	const output = "booting\nKINDLING_SSH_HOST_PUBLIC_KEY=ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e sandbox@example\n"
	got, err := ExtractMarkedAuthorizedKey(output, "KINDLING_SSH_HOST_PUBLIC_KEY=")
	if err != nil {
		t.Fatalf("ExtractMarkedAuthorizedKey: %v", err)
	}
	const want = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e"
	if got != want {
		t.Fatalf("host key = %q, want %q", got, want)
	}
}

func TestKnownHostsLine(t *testing.T) {
	t.Parallel()

	got, err := KnownHostsLine("sandbox-12345678", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e sandbox@example")
	if err != nil {
		t.Fatalf("KnownHostsLine: %v", err)
	}
	const want = "sandbox-12345678 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e\n"
	if got != want {
		t.Fatalf("known_hosts line = %q, want %q", got, want)
	}
}
