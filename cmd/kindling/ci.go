package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/spf13/cobra"
)

func ciCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Run repository workflows with the Kindling workflow engine",
	}
	cmd.AddCommand(ciListWorkflowsCmd())
	cmd.AddCommand(ciRunCmd())
	return cmd
}

func ciListWorkflowsCmd() *cobra.Command {
	var cwd string
	cmd := &cobra.Command{
		Use:   "list-workflows",
		Short: "List workflows discovered under .github/workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolver := ci.NewFSWorkflowResolver()
			repoRoot, err := resolver.FindRepoRoot(cwd)
			if err != nil {
				return err
			}
			workflows, err := resolver.List(repoRoot)
			if err != nil {
				return err
			}
			if len(workflows) == 0 {
				fmt.Println("No workflows found.")
				return nil
			}
			for _, wf := range workflows {
				fmt.Printf("%s\t%s\t%s\n", wf.Stem, wf.Name, wf.Filename)
				jobIDs := make([]string, 0, len(wf.Jobs))
				for jobID := range wf.Jobs {
					jobIDs = append(jobIDs, jobID)
				}
				sort.Strings(jobIDs)
				for _, jobID := range jobIDs {
					job := wf.Jobs[jobID]
					fmt.Printf("  %s\t%s\n", jobID, job.Name)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", ".", "Directory to start repo discovery from")
	return cmd
}

func ciRunCmd() *cobra.Command {
	var (
		cwd    string
		jobID  string
		event  string
		inputs []string
	)
	cmd := &cobra.Command{
		Use:   "run <workflow>",
		Short: "Run a workflow or selected job from .github/workflows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolver := ci.NewFSWorkflowResolver()
			repoRoot, err := resolver.FindRepoRoot(cwd)
			if err != nil {
				return err
			}
			workflow, err := resolver.Resolve(repoRoot, args[0])
			if err != nil {
				return err
			}
			compiler := ci.NewStaticWorkflowCompiler()
			plan, err := compiler.Compile(ci.CompileRequest{
				Workflow: workflow,
				JobID:    strings.TrimSpace(jobID),
				Event:    strings.TrimSpace(event),
				Inputs:   parseCLIInputs(inputs),
				RepoRoot: repoRoot,
			})
			if err != nil {
				return err
			}

			runner := ci.NewLocalWorkflowRunner()
			result, err := runner.Run(context.Background(), plan, ci.RunOptions{
				Stdout: os.Stdout,
				Stderr: os.Stderr,
			})
			if len(result.Artifacts) > 0 {
				fmt.Println("Artifacts:")
				for _, artifact := range result.Artifacts {
					fmt.Printf("  %s\t%s\n", artifact.Name, artifact.Path)
				}
			}
			if err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", ".", "Directory to start repo discovery from")
	cmd.Flags().StringVar(&jobID, "job", "", "Specific job to run (dependencies run first)")
	cmd.Flags().StringVar(&event, "event", "", "Workflow event name to emulate (defaults from workflow triggers)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "workflow_dispatch input in key=value form")
	return cmd
}

func parseCLIInputs(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		if i := strings.IndexByte(value, '='); i > 0 {
			out[strings.TrimSpace(value[:i])] = strings.TrimSpace(value[i+1:])
		}
	}
	return out
}
