package ci

import "testing"

func TestStaticWorkflowCompiler_CompileDeployWorkflowDependencyOrder(t *testing.T) {
	t.Parallel()

	resolver := NewFSWorkflowResolver()
	repoRoot, err := resolver.FindRepoRoot(".")
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	workflow, err := resolver.Resolve(repoRoot, "deploy-prod")
	if err != nil {
		t.Fatalf("resolve workflow: %v", err)
	}
	compiler := NewStaticWorkflowCompiler()
	plan, err := compiler.Compile(CompileRequest{
		Workflow: workflow,
		JobID:    "deploy",
		RepoRoot: repoRoot,
	})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	got := make([]string, 0, len(plan.Jobs))
	for _, job := range plan.Jobs {
		got = append(got, job.ID)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 jobs, got %d (%v)", len(got), got)
	}
	index := map[string]int{}
	for i, id := range got {
		index[id] = i
	}
	for _, id := range []string{"paths-filter", "package", "kernel", "initramfs", "deploy"} {
		if _, ok := index[id]; !ok {
			t.Fatalf("missing job %q in plan order %v", id, got)
		}
	}
	if index["paths-filter"] > index["deploy"] || index["package"] > index["deploy"] || index["kernel"] > index["deploy"] || index["initramfs"] > index["deploy"] {
		t.Fatalf("expected dependencies before deploy, got %v", got)
	}
	if plan.Event != "workflow_dispatch" {
		t.Fatalf("expected default event workflow_dispatch, got %q", plan.Event)
	}
}

func TestEvaluatePathsFilter(t *testing.T) {
	t.Parallel()

	outputs, err := evaluatePathsFilter(`
kernel:
  - 'scripts/build-kernel.sh'
initramfs:
  - 'cmd/guest-agent/**'
  - 'scripts/build-initramfs.sh'
`, []string{"cmd/guest-agent/main.go"})
	if err != nil {
		t.Fatalf("evaluate paths filter: %v", err)
	}
	if outputs["kernel"] != "false" {
		t.Fatalf("expected kernel=false, got %q", outputs["kernel"])
	}
	if outputs["initramfs"] != "true" {
		t.Fatalf("expected initramfs=true, got %q", outputs["initramfs"])
	}
}
