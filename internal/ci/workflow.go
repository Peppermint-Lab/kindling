package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowResolver discovers and loads workflow definitions from .github/workflows.
type WorkflowResolver interface {
	FindRepoRoot(start string) (string, error)
	List(repoRoot string) ([]Workflow, error)
	Resolve(repoRoot, workflowRef string) (Workflow, error)
}

type FSWorkflowResolver struct{}

func NewFSWorkflowResolver() *FSWorkflowResolver {
	return &FSWorkflowResolver{}
}

type Workflow struct {
	Filepath      string
	Filename      string
	Stem          string
	Name          string
	RawOn         yaml.Node
	Triggers      WorkflowTriggers
	Jobs          map[string]WorkflowJob
	DispatchInput map[string]WorkflowDispatchInput
}

type WorkflowTriggers struct {
	Push             bool
	PullRequest      bool
	WorkflowDispatch bool
}

type WorkflowDispatchInput struct {
	Description string
	Default     string
	Type        string
	Required    bool
}

type WorkflowJob struct {
	ID       string
	Name     string            `yaml:"name"`
	Needs    StringOrSlice     `yaml:"needs"`
	If       string            `yaml:"if"`
	RunsOn   StringOrSlice     `yaml:"runs-on"`
	Env      map[string]string `yaml:"env"`
	Defaults WorkflowDefaults  `yaml:"defaults"`
	Outputs  map[string]string `yaml:"outputs"`
	Steps    []WorkflowStep    `yaml:"steps"`
}

type WorkflowDefaults struct {
	Run WorkflowRunDefaults `yaml:"run"`
}

type WorkflowRunDefaults struct {
	WorkingDirectory string `yaml:"working-directory"`
	Shell            string `yaml:"shell"`
}

type WorkflowStep struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	If               string            `yaml:"if"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	With             map[string]string `yaml:"with"`
}

type StringOrSlice []string

func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		if strings.TrimSpace(single) == "" {
			*s = nil
			return nil
		}
		*s = []string{single}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	case 0:
		*s = nil
		return nil
	default:
		return fmt.Errorf("expected string or string list, got yaml kind %d", value.Kind)
	}
}

func (r *FSWorkflowResolver) FindRepoRoot(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		start = "."
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("abs path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	dir := abs
	for {
		workflowsDir := filepath.Join(dir, ".github", "workflows")
		if st, err := os.Stat(workflowsDir); err == nil && st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .github/workflows found from %s upward", abs)
		}
		dir = parent
	}
}

func (r *FSWorkflowResolver) List(repoRoot string) ([]Workflow, error) {
	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read workflows dir: %w", err)
	}
	workflows := make([]Workflow, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		wf, err := loadWorkflow(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, wf)
	}
	sort.Slice(workflows, func(i, j int) bool {
		if workflows[i].Name == workflows[j].Name {
			return workflows[i].Filename < workflows[j].Filename
		}
		return workflows[i].Name < workflows[j].Name
	})
	return workflows, nil
}

func (r *FSWorkflowResolver) Resolve(repoRoot, workflowRef string) (Workflow, error) {
	workflows, err := r.List(repoRoot)
	if err != nil {
		return Workflow{}, err
	}
	ref := strings.TrimSpace(strings.ToLower(workflowRef))
	if ref == "" {
		return Workflow{}, fmt.Errorf("workflow reference is required")
	}
	matches := make([]Workflow, 0, 2)
	for _, wf := range workflows {
		if strings.EqualFold(wf.Stem, workflowRef) || strings.EqualFold(wf.Name, workflowRef) || strings.EqualFold(wf.Filename, workflowRef) {
			matches = append(matches, wf)
		}
		if strings.ToLower(wf.Stem) == ref || strings.ToLower(wf.Name) == ref {
			matches = appendUniqueWorkflow(matches, wf)
		}
	}
	if len(matches) == 0 {
		available := make([]string, 0, len(workflows))
		for _, wf := range workflows {
			available = append(available, fmt.Sprintf("%s (%s)", wf.Stem, wf.Name))
		}
		return Workflow{}, fmt.Errorf("workflow %q not found; available: %s", workflowRef, strings.Join(available, ", "))
	}
	if len(matches) > 1 {
		options := make([]string, 0, len(matches))
		for _, wf := range matches {
			options = append(options, fmt.Sprintf("%s (%s)", wf.Stem, wf.Name))
		}
		return Workflow{}, fmt.Errorf("workflow %q is ambiguous: %s", workflowRef, strings.Join(options, ", "))
	}
	return matches[0], nil
}

func appendUniqueWorkflow(list []Workflow, wf Workflow) []Workflow {
	for _, existing := range list {
		if existing.Filepath == wf.Filepath {
			return list
		}
	}
	return append(list, wf)
}

func loadWorkflow(path string) (Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Workflow{}, fmt.Errorf("read workflow %s: %w", path, err)
	}
	var raw struct {
		Name string                 `yaml:"name"`
		On   yaml.Node              `yaml:"on"`
		Jobs map[string]WorkflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Workflow{}, fmt.Errorf("parse workflow %s: %w", path, err)
	}
	filename := filepath.Base(path)
	stem := strings.TrimSuffix(strings.TrimSuffix(filename, ".yaml"), ".yml")
	if strings.TrimSpace(raw.Name) == "" {
		raw.Name = stem
	}
	for id, job := range raw.Jobs {
		job.ID = id
		if strings.TrimSpace(job.Name) == "" {
			job.Name = id
		}
		for i := range job.Steps {
			if strings.TrimSpace(job.Steps[i].Name) == "" {
				switch {
				case strings.TrimSpace(job.Steps[i].Uses) != "":
					job.Steps[i].Name = job.Steps[i].Uses
				case strings.TrimSpace(job.Steps[i].Run) != "":
					job.Steps[i].Name = "run"
				default:
					job.Steps[i].Name = fmt.Sprintf("step-%d", i+1)
				}
			}
		}
		raw.Jobs[id] = job
	}
	triggers, inputs, err := parseWorkflowTriggers(raw.On)
	if err != nil {
		return Workflow{}, fmt.Errorf("parse workflow triggers %s: %w", path, err)
	}
	return Workflow{
		Filepath:      path,
		Filename:      filename,
		Stem:          stem,
		Name:          raw.Name,
		RawOn:         raw.On,
		Triggers:      triggers,
		Jobs:          raw.Jobs,
		DispatchInput: inputs,
	}, nil
}

func parseWorkflowTriggers(node yaml.Node) (WorkflowTriggers, map[string]WorkflowDispatchInput, error) {
	inputs := map[string]WorkflowDispatchInput{}
	if node.Kind == 0 {
		return WorkflowTriggers{}, inputs, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		var event string
		if err := node.Decode(&event); err != nil {
			return WorkflowTriggers{}, nil, err
		}
		return triggerSet([]string{event}), inputs, nil
	case yaml.SequenceNode:
		var events []string
		if err := node.Decode(&events); err != nil {
			return WorkflowTriggers{}, nil, err
		}
		return triggerSet(events), inputs, nil
	case yaml.MappingNode:
		var triggers WorkflowTriggers
		for i := 0; i < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			val := node.Content[i+1]
			switch key {
			case "push":
				triggers.Push = true
			case "pull_request":
				triggers.PullRequest = true
			case "workflow_dispatch":
				triggers.WorkflowDispatch = true
				parsedInputs, err := parseDispatchInputs(val)
				if err != nil {
					return WorkflowTriggers{}, nil, err
				}
				inputs = parsedInputs
			}
		}
		return triggers, inputs, nil
	default:
		return WorkflowTriggers{}, nil, fmt.Errorf("unsupported on: yaml kind %d", node.Kind)
	}
}

func triggerSet(events []string) WorkflowTriggers {
	var out WorkflowTriggers
	for _, event := range events {
		switch strings.TrimSpace(event) {
		case "push":
			out.Push = true
		case "pull_request":
			out.PullRequest = true
		case "workflow_dispatch":
			out.WorkflowDispatch = true
		}
	}
	return out
}

func parseDispatchInputs(node *yaml.Node) (map[string]WorkflowDispatchInput, error) {
	inputs := map[string]WorkflowDispatchInput{}
	if node == nil || node.Kind != yaml.MappingNode {
		return inputs, nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		val := node.Content[i+1]
		if key != "inputs" || val.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j < len(val.Content); j += 2 {
			name := strings.TrimSpace(val.Content[j].Value)
			specNode := val.Content[j+1]
			var spec struct {
				Description string      `yaml:"description"`
				Default     interface{} `yaml:"default"`
				Type        string      `yaml:"type"`
				Required    bool        `yaml:"required"`
			}
			if err := specNode.Decode(&spec); err != nil {
				return nil, err
			}
			defaultValue := ""
			if spec.Default != nil {
				defaultValue = fmt.Sprint(spec.Default)
			}
			inputs[name] = WorkflowDispatchInput{
				Description: spec.Description,
				Default:     defaultValue,
				Type:        spec.Type,
				Required:    spec.Required,
			}
		}
	}
	return inputs, nil
}
