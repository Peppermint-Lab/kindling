//go:build !darwin

package ci

func NewPreferredWorkflowRunner() WorkflowRunner {
	return NewLocalWorkflowRunner()
}
