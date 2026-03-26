package builder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceWorkspaceFromSource(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceWorkspaceFromSource(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "FROM scratch\n" {
		t.Fatalf("got %q", string(b))
	}
}
