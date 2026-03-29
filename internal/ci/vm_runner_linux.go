//go:build linux

package ci

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kindlingvm/kindling/internal/builder"
)

type CloudHypervisorWorkflowRunner struct {
	exec *builder.CloudHypervisorExecRunner
}

type RunnerSelection struct {
	RequireMicroVM bool
}

func NewWorkflowRunner(sel RunnerSelection) (WorkflowRunner, error) {
	runner, err := builder.NewCloudHypervisorExecRunner(builder.CloudHypervisorExecRunnerConfig{})
	if err == nil {
		return &CloudHypervisorWorkflowRunner{exec: runner}, nil
	}
	if sel.RequireMicroVM {
		return nil, fmt.Errorf("microVM CI execution is required, but the cloud-hypervisor runner is unavailable: %w", err)
	}
	return NewLocalWorkflowRunner(), nil
}

func NewPreferredWorkflowRunner() WorkflowRunner {
	r, err := NewWorkflowRunner(RunnerSelection{})
	if err != nil {
		return NewLocalWorkflowRunner()
	}
	return r
}

func (r *CloudHypervisorWorkflowRunner) Backend() string { return "cloud_hypervisor" }

func (r *CloudHypervisorWorkflowRunner) IsMicroVM() bool { return true }

func (r *CloudHypervisorWorkflowRunner) Run(ctx context.Context, plan ExecutionPlan, opts RunOptions) (RunResult, error) {
	workspaceRoot, cleanup, err := prepareVMWorkspace(plan.RepoRoot)
	if err != nil {
		return RunResult{}, err
	}
	defer cleanup()
	plan.RepoRoot = workspaceRoot

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	artifacts, err := newArtifactStore()
	if err != nil {
		return RunResult{}, err
	}
	cache, err := newCacheStore()
	if err != nil {
		return RunResult{}, err
	}

	ctxState := evalContext{
		repoRoot: plan.RepoRoot,
		plan:     plan,
		env:      buildBaseEnv(opts.Env),
		steps:    map[string]stepState{},
		needs:    map[string]jobState{},
	}
	jobResults := make([]JobRunResult, 0, len(plan.Jobs))

	for _, job := range plan.Jobs {
		jobCtx := ctxState
		jobCtx.steps = map[string]stepState{}
		if shouldSkip, err := shouldSkipExpr(job.If, jobCtx); err != nil {
			return RunResult{}, fmt.Errorf("job %s if: %w", job.ID, err)
		} else if shouldSkip {
			ctxState.needs[job.ID] = jobState{Result: "skipped", Outputs: map[string]string{}}
			jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: "skipped", Outputs: map[string]string{}})
			fmt.Fprintf(stdout, "==> Job %s skipped\n", job.ID)
			continue
		}
		fmt.Fprintf(stdout, "==> Job %s (%s)\n", job.ID, job.Name)
		jobEnv := buildProcessEnv(ctxState.env, job.Env, nil, jobCtx)
		jobResult := "success"
		for _, step := range job.Steps {
			if skip, err := shouldSkipExpr(step.If, jobCtx); err != nil {
				return RunResult{}, fmt.Errorf("job %s step %s if: %w", job.ID, step.Name, err)
			} else if skip {
				fmt.Fprintf(stdout, "----> Step %s skipped\n", step.Name)
				continue
			}
			fmt.Fprintf(stdout, "----> Step %s\n", step.Name)
			stepEnv := cloneMap(jobEnv)
			mergeEnv(stepEnv, step.Env)
			if err := interpolateEnv(stepEnv, jobCtx); err != nil {
				return RunResult{}, fmt.Errorf("job %s step %s env: %w", job.ID, step.Name, err)
			}
			outputs, err := r.runStep(ctx, plan.RepoRoot, step, stepEnv, stdout, stderr, jobCtx, artifacts, cache)
			if err != nil {
				jobResult = "failure"
				ctxState.needs[job.ID] = jobState{Result: jobResult, Outputs: map[string]string{}}
				jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: jobResult, Outputs: map[string]string{}})
				return RunResult{Jobs: jobResults, Artifacts: artifacts.List(), ArtifactRoot: artifacts.root, Backend: r.Backend()}, fmt.Errorf("job %s step %s: %w", job.ID, step.Name, err)
			}
			if step.ID != "" {
				jobCtx.steps[step.ID] = stepState{Outputs: outputs}
			}
		}
		outputs, err := evaluateJobOutputs(job.Outputs, jobCtx)
		if err != nil {
			return RunResult{}, fmt.Errorf("job %s outputs: %w", job.ID, err)
		}
		ctxState.needs[job.ID] = jobState{Result: jobResult, Outputs: outputs}
		jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: jobResult, Outputs: outputs})
		for _, entry := range cache.entries {
			_ = cache.save(entry.Key, filepath.Join(plan.RepoRoot, entry.Path))
		}
	}
	return RunResult{Jobs: jobResults, Artifacts: artifacts.List(), ArtifactRoot: artifacts.root, Backend: r.Backend()}, nil
}

func (r *CloudHypervisorWorkflowRunner) runStep(ctx context.Context, repoRoot string, step CompiledStep, env map[string]string, stdout, stderr io.Writer, ev evalContext, artifacts *artifactStore, cache *cacheStore) (map[string]string, error) {
	switch step.Kind {
	case StepKindCheckout:
		return map[string]string{}, nil
	case StepKindSetupGo:
		return map[string]string{}, r.requireGuestCommand(ctx, repoRoot, "go")
	case StepKindSetupNode:
		if err := r.requireGuestCommand(ctx, repoRoot, "node"); err != nil {
			return nil, err
		}
		return map[string]string{}, r.requireGuestCommand(ctx, repoRoot, "npm")
	case StepKindUploadArtifact:
		name, err := interpolateValue(step.With["name"], ev)
		if err != nil {
			return nil, err
		}
		pathValue, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, err
		}
		src := filepath.Join(repoRoot, pathValue)
		if err := artifacts.Save(name, src); err != nil {
			return nil, err
		}
		return map[string]string{}, nil
	case StepKindDownloadArtifact:
		name, err := interpolateValue(step.With["name"], ev)
		if err != nil {
			return nil, err
		}
		target, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(target) == "" {
			target = "."
		}
		src, ok := artifacts.Path(name)
		if !ok {
			return nil, fmt.Errorf("artifact %q not found", name)
		}
		return map[string]string{}, copyArtifactContents(src, filepath.Join(repoRoot, target))
	case StepKindCache:
		pathValue, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, err
		}
		keyValue, err := interpolateValue(step.With["key"], ev)
		if err != nil {
			return nil, err
		}
		if err := cache.restore(keyValue, filepath.Join(repoRoot, pathValue)); err != nil {
			return nil, err
		}
		cache.track(keyValue, pathValue)
		return map[string]string{}, nil
	case StepKindPathsFilter:
		filters, err := interpolateValue(step.With["filters"], ev)
		if err != nil {
			return nil, err
		}
		changed, err := gitChangedFiles(ctx, repoRoot)
		if err != nil {
			return nil, err
		}
		return evaluatePathsFilter(filters, changed)
	case StepKindSSHAgent:
		key, err := interpolateValue(step.With["ssh-private-key"], ev)
		if err != nil {
			return nil, err
		}
		script := `
set -euo pipefail
eval "$(ssh-agent -s)"
printf '%s\n' "$KINDLING_SSH_PRIVATE_KEY" | ssh-add -
echo "ssh_auth_sock=$SSH_AUTH_SOCK" >> "$GITHUB_OUTPUT"
echo "ssh_agent_pid=$SSH_AGENT_PID" >> "$GITHUB_OUTPUT"
`
		out, err := r.execShellStep(ctx, repoRoot, "/", script, withEnv(env, "KINDLING_SSH_PRIVATE_KEY="+key), func(line string) {
			if stdout != nil {
				fmt.Fprintln(stdout, line)
			}
		})
		if err != nil {
			return nil, err
		}
		env["SSH_AUTH_SOCK"] = out["ssh_auth_sock"]
		env["SSH_AGENT_PID"] = out["ssh_agent_pid"]
		return map[string]string{}, nil
	case StepKindRun:
		outputs, err := r.execShellStep(ctx, repoRoot, step.WorkingDirectory, step.Run, env, func(line string) {
			if stdout != nil {
				fmt.Fprintln(stdout, line)
			}
		})
		return outputs, err
	default:
		return nil, fmt.Errorf("unsupported step kind %s", step.Kind)
	}
}

func (r *CloudHypervisorWorkflowRunner) requireGuestCommand(ctx context.Context, repoRoot, name string) error {
	_, err := r.exec.Exec(ctx, builder.ExecRun{
		WorkspaceDir: repoRoot,
		Cwd:          "/workspace",
		Argv:         []string{"sh", "-c", "command -v " + name},
		Env:          []string{"PATH=/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin"},
	})
	return err
}

func (r *CloudHypervisorWorkflowRunner) execShellStep(ctx context.Context, repoRoot, workingDir, script string, env map[string]string, logLine func(string)) (map[string]string, error) {
	outputFile := filepath.Join(repoRoot, ".kindling-gh-output")
	_ = os.Remove(outputFile)
	cwd := "/workspace"
	if strings.TrimSpace(workingDir) != "" && workingDir != "/" {
		cwd = filepath.ToSlash(filepath.Join("/workspace", workingDir))
	}
	envList := mapToEnv(env)
	envList = append(envList, "GITHUB_OUTPUT=/workspace/.kindling-gh-output", "GITHUB_WORKSPACE=/workspace")
	code, err := r.exec.Exec(ctx, builder.ExecRun{
		WorkspaceDir: repoRoot,
		Cwd:          cwd,
		Argv:         []string{"sh", "-lc", script},
		Env:          envList,
		LogLine:      logLine,
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("command exited with code %d", code)
	}
	outputs, err := parseOutputFile(outputFile)
	_ = os.Remove(outputFile)
	return outputs, err
}

func withEnv(env map[string]string, extra ...string) map[string]string {
	out := cloneMap(env)
	for _, kv := range extra {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}
