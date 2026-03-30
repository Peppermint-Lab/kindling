package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateServerID_CreatesAndReusesHomeFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	first, err := LoadOrCreateServerID()
	if err != nil {
		t.Fatalf("first LoadOrCreateServerID: %v", err)
	}
	second, err := LoadOrCreateServerID()
	if err != nil {
		t.Fatalf("second LoadOrCreateServerID: %v", err)
	}
	if first != second {
		t.Fatalf("server ID changed across loads: %s != %s", first, second)
	}

	path := filepath.Join(dir, LocalServerIDRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read server id file: %v", err)
	}
	if got := string(raw); got != first.String() {
		t.Fatalf("server id file = %q, want %q", got, first.String())
	}
}

func TestLoadOrCreateServerID_CorruptFileFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path := filepath.Join(dir, LocalServerIDRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-a-uuid"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateServerID(); err == nil {
		t.Fatal("expected corrupt server-id file to fail")
	}
}
