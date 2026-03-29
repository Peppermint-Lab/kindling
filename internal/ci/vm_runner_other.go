//go:build !darwin

package ci

import "fmt"

type RunnerSelection struct {
	RequireMicroVM bool
}

func NewWorkflowRunner(sel RunnerSelection) (WorkflowRunner, error) {
	if sel.RequireMicroVM {
		return nil, fmt.Errorf("microVM CI execution is required, but no microVM workflow runner is implemented on this host")
	}
	return NewLocalWorkflowRunner(), nil
}

func NewPreferredWorkflowRunner() WorkflowRunner {
	r, err := NewWorkflowRunner(RunnerSelection{})
	if err != nil {
		return &LocalWorkflowRunner{}
	}
	return r
}
