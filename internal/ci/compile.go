package ci

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// WorkflowCompiler validates and lowers a workflow into an execution plan.
type WorkflowCompiler interface {
	Compile(req CompileRequest) (ExecutionPlan, error)
}

type StaticWorkflowCompiler struct{}

func NewStaticWorkflowCompiler() *StaticWorkflowCompiler {
	return &StaticWorkflowCompiler{}
}

type CompileRequest struct {
	Workflow Workflow
	JobID    string
	Event    string
	Inputs   map[string]string
	RepoRoot string
}

type ExecutionPlan struct {
	WorkflowName string
	WorkflowFile string
	Event        string
	RepoRoot     string
	Inputs       map[string]string
	Jobs         []CompiledJob
}

type CompiledJob struct {
	ID      string
	Name    string
	Needs   []string
	If      string
	Outputs map[string]string
	Env     map[string]string
	Steps   []CompiledStep
}

type StepKind string

const (
	StepKindRun              StepKind = "run"
	StepKindCheckout         StepKind = "checkout"
	StepKindSetupGo          StepKind = "setup_go"
	StepKindSetupNode        StepKind = "setup_node"
	StepKindUploadArtifact   StepKind = "upload_artifact"
	StepKindDownloadArtifact StepKind = "download_artifact"
	StepKindCache            StepKind = "cache"
	StepKindPathsFilter      StepKind = "paths_filter"
	StepKindSSHAgent         StepKind = "ssh_agent"
)

type CompiledStep struct {
	ID               string
	Name             string
	If               string
	Kind             StepKind
	Uses             string
	Run              string
	WorkingDirectory string
	Env              map[string]string
	With             map[string]string
}

func (*StaticWorkflowCompiler) Compile(req CompileRequest) (ExecutionPlan, error) {
	event := strings.TrimSpace(req.Event)
	if event == "" {
		event = defaultWorkflowEvent(req.Workflow)
	}
	if event == "" {
		return ExecutionPlan{}, fmt.Errorf("could not determine default event for workflow %s", req.Workflow.Name)
	}

	inputs := make(map[string]string, len(req.Workflow.DispatchInput)+len(req.Inputs))
	for name, spec := range req.Workflow.DispatchInput {
		if spec.Default != "" {
			inputs[name] = spec.Default
		}
	}
	for key, value := range req.Inputs {
		inputs[key] = value
	}
	for name, spec := range req.Workflow.DispatchInput {
		if spec.Required && strings.TrimSpace(inputs[name]) == "" {
			return ExecutionPlan{}, fmt.Errorf("workflow input %q is required", name)
		}
	}

	jobOrder, err := resolveJobOrder(req.Workflow, req.JobID)
	if err != nil {
		return ExecutionPlan{}, err
	}
	compiled := make([]CompiledJob, 0, len(jobOrder))
	for _, jobID := range jobOrder {
		job := req.Workflow.Jobs[jobID]
		steps := make([]CompiledStep, 0, len(job.Steps))
		for i, step := range job.Steps {
			cs, err := compileStep(job, step, i)
			if err != nil {
				return ExecutionPlan{}, fmt.Errorf("job %s step %q: %w", jobID, step.Name, err)
			}
			steps = append(steps, cs)
		}
		compiled = append(compiled, CompiledJob{
			ID:      job.ID,
			Name:    job.Name,
			Needs:   append([]string(nil), job.Needs...),
			If:      strings.TrimSpace(job.If),
			Outputs: cloneMap(job.Outputs),
			Env:     cloneMap(job.Env),
			Steps:   steps,
		})
	}

	return ExecutionPlan{
		WorkflowName: req.Workflow.Name,
		WorkflowFile: filepath.Base(req.Workflow.Filepath),
		Event:        event,
		RepoRoot:     req.RepoRoot,
		Inputs:       inputs,
		Jobs:         compiled,
	}, nil
}

func resolveJobOrder(workflow Workflow, selectedJob string) ([]string, error) {
	if strings.TrimSpace(selectedJob) != "" {
		if _, ok := workflow.Jobs[selectedJob]; !ok {
			return nil, fmt.Errorf("job %q not found in workflow %s", selectedJob, workflow.Name)
		}
		return topoSortJobs(workflow.Jobs, selectedJob)
	}
	ids := make([]string, 0, len(workflow.Jobs))
	for id := range workflow.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return topoSortJobs(workflow.Jobs, ids...)
}

func topoSortJobs(jobs map[string]WorkflowJob, roots ...string) ([]string, error) {
	visited := map[string]bool{}
	temp := map[string]bool{}
	out := make([]string, 0, len(jobs))
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		if temp[id] {
			return fmt.Errorf("cycle detected at job %s", id)
		}
		job, ok := jobs[id]
		if !ok {
			return fmt.Errorf("job %s not found", id)
		}
		temp[id] = true
		needs := append([]string(nil), job.Needs...)
		sort.Strings(needs)
		for _, dep := range needs {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(temp, id)
		visited[id] = true
		out = append(out, id)
		return nil
	}

	if len(roots) == 0 {
		for id := range jobs {
			roots = append(roots, id)
		}
		sort.Strings(roots)
	}
	for _, id := range roots {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func compileStep(job WorkflowJob, step WorkflowStep, idx int) (CompiledStep, error) {
	cs := CompiledStep{
		ID:               strings.TrimSpace(step.ID),
		Name:             step.Name,
		If:               strings.TrimSpace(step.If),
		Uses:             strings.TrimSpace(step.Uses),
		Run:              step.Run,
		WorkingDirectory: firstNonEmpty(strings.TrimSpace(step.WorkingDirectory), strings.TrimSpace(job.Defaults.Run.WorkingDirectory)),
		Env:              cloneMap(step.Env),
		With:             cloneMap(step.With),
	}
	switch {
	case strings.TrimSpace(step.Run) != "":
		cs.Kind = StepKindRun
		if cs.ID == "" {
			cs.ID = fmt.Sprintf("run_%d", idx+1)
		}
		return cs, nil
	case strings.TrimSpace(step.Uses) == "":
		return CompiledStep{}, fmt.Errorf("step must define run or uses")
	}

	base := normalizeUses(step.Uses)
	switch base {
	case "actions/checkout":
		cs.Kind = StepKindCheckout
	case "actions/setup-go":
		cs.Kind = StepKindSetupGo
	case "actions/setup-node":
		cs.Kind = StepKindSetupNode
	case "actions/upload-artifact":
		cs.Kind = StepKindUploadArtifact
	case "actions/download-artifact":
		cs.Kind = StepKindDownloadArtifact
	case "actions/cache":
		cs.Kind = StepKindCache
	case "dorny/paths-filter":
		cs.Kind = StepKindPathsFilter
	case "webfactory/ssh-agent":
		cs.Kind = StepKindSSHAgent
	default:
		return CompiledStep{}, fmt.Errorf("unsupported action %q", step.Uses)
	}
	if cs.ID == "" {
		cs.ID = fmt.Sprintf("%s_%d", strings.ReplaceAll(string(cs.Kind), "-", "_"), idx+1)
	}
	return cs, nil
}

func normalizeUses(uses string) string {
	trimmed := strings.TrimSpace(strings.ToLower(uses))
	if i := strings.IndexByte(trimmed, '@'); i >= 0 {
		trimmed = trimmed[:i]
	}
	return trimmed
}

func defaultWorkflowEvent(workflow Workflow) string {
	switch {
	case workflow.Triggers.WorkflowDispatch:
		return "workflow_dispatch"
	case workflow.Triggers.Push:
		return "push"
	case workflow.Triggers.PullRequest:
		return "pull_request"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func containsJob(plan ExecutionPlan, id string) bool {
	return slices.ContainsFunc(plan.Jobs, func(job CompiledJob) bool { return job.ID == id })
}
