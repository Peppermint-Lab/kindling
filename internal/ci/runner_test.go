package ci

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalWorkflowRunner_RunFixtureWorkflow(t *testing.T) {
	t.Parallel()

	for _, bin := range []string{"git", "go", "node", "npm", "bash"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}

	repoRoot := t.TempDir()
	workflowsDir := filepath.Join(repoRoot, ".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "fixture.yml"), []byte(`
name: Fixture
on:
  workflow_dispatch:
jobs:
  prep:
    runs-on: ubuntu-latest
    outputs:
      ready: ${{ steps.meta.outputs.ready }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
      - id: meta
        run: |
          mkdir -p out
          echo "hello" > out/result.txt
          echo "ready=true" >> "$GITHUB_OUTPUT"
      - uses: actions/upload-artifact@v4
        with:
          name: bundle
          path: out
  consume:
    runs-on: ubuntu-latest
    needs: [prep]
    if: needs.prep.outputs.ready == 'true'
    steps:
      - uses: actions/download-artifact@v4
        with:
          name: bundle
          path: restored
      - run: |
          test -f restored/result.txt
          grep hello restored/result.txt
`), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module fixture\n\ngo 1.26.1\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	init := exec.Command("git", "init")
	init.Dir = repoRoot
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	resolver := NewFSWorkflowResolver()
	workflow, err := resolver.Resolve(repoRoot, "fixture")
	if err != nil {
		t.Fatalf("resolve workflow: %v", err)
	}
	compiler := NewStaticWorkflowCompiler()
	plan, err := compiler.Compile(CompileRequest{
		Workflow: workflow,
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}

	var stdout bytes.Buffer
	runner := NewLocalWorkflowRunner()
	result, err := runner.Run(context.Background(), plan, RunOptions{
		Stdout: &stdout,
		Stderr: &stdout,
	})
	if err != nil {
		t.Fatalf("run workflow: %v\n%s", err, stdout.String())
	}
	if len(result.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(result.Jobs))
	}
	if got := result.Jobs[0].Outputs["ready"]; got != "true" {
		t.Fatalf("expected prep ready=true, got %q", got)
	}
	if !strings.Contains(stdout.String(), "hello") {
		t.Fatalf("expected consume job output in logs, got %s", stdout.String())
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "bundle" {
		t.Fatalf("expected bundle artifact, got %#v", result.Artifacts)
	}
}
