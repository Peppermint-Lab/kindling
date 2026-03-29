package ci

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFSWorkflowResolver_ListAndResolveRepoWorkflows(t *testing.T) {
	t.Parallel()

	resolver := NewFSWorkflowResolver()
	repoRoot, err := resolver.FindRepoRoot(".")
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workflows, err := resolver.List(repoRoot)
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(workflows) < 2 {
		t.Fatalf("expected repo workflows, got %d", len(workflows))
	}
	ciWorkflow, err := resolver.Resolve(repoRoot, "ci")
	if err != nil {
		t.Fatalf("resolve ci workflow: %v", err)
	}
	if ciWorkflow.Name != "CI" {
		t.Fatalf("expected workflow name CI, got %q", ciWorkflow.Name)
	}
	if _, ok := ciWorkflow.Jobs["go"]; !ok {
		t.Fatalf("expected go job in ci workflow")
	}
	if _, ok := ciWorkflow.Jobs["dashboard"]; !ok {
		t.Fatalf("expected dashboard job in ci workflow")
	}
}

func TestFSWorkflowResolver_FindRepoRootFromNestedPath(t *testing.T) {
	t.Parallel()

	resolver := NewFSWorkflowResolver()
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	found, err := resolver.FindRepoRoot(nested)
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	if found != root {
		t.Fatalf("expected %s, got %s", root, found)
	}
}
