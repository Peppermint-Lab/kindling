package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePostgresDSN_Override(t *testing.T) {
	got, err := ResolvePostgresDSN("postgres://custom/db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://custom/db" {
		t.Fatalf("got %q", got)
	}
}

func TestResolvePostgresDSN_HomeFile(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists; home file would be ignored")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	p := filepath.Join(dir, LocalDSNRelPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	want := "postgres://fromfile/test"
	if err := os.WriteFile(p, []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolvePostgresDSN_Default(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	got, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultLocalPostgresDSN {
		t.Fatalf("got %q", got)
	}
}
