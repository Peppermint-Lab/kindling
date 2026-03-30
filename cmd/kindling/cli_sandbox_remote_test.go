package main

import (
	"os"
	"strings"
	"testing"
)

func TestRemoteVMSSHHostAlias(t *testing.T) {
	t.Parallel()

	if got := remoteVMSSHHostAlias("12345678-1234-1234-1234-123456789abc"); got != "vm-12345678" {
		t.Fatalf("remoteVMSSHHostAlias = %q", got)
	}
}

func TestWriteSandboxKnownHosts(t *testing.T) {
	t.Parallel()

	path, err := writeSandboxKnownHosts("12345678-1234-1234-1234-123456789abc", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e sandbox@example")
	if err != nil {
		t.Fatalf("writeSandboxKnownHosts: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "vm-12345678 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB4P80v4v+8WgM6eTHAiSl4KjkJN96YjKf0CrQxQcf7e") {
		t.Fatalf("known_hosts content = %q", got)
	}
}

func TestWriteSandboxKnownHostsRequiresManagedKey(t *testing.T) {
	t.Parallel()

	if _, err := writeSandboxKnownHosts("12345678-1234-1234-1234-123456789abc", ""); err == nil {
		t.Fatal("expected error when remote VM host key is missing")
	}
}
