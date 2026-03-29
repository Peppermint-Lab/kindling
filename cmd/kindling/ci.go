package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

func ciCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Run repository workflows with the Kindling workflow engine",
	}
	cmd.AddCommand(ciListWorkflowsCmd())
	cmd.AddCommand(ciRunCmd())
	cmd.AddCommand(ciGetCmd())
	cmd.AddCommand(ciLogsCmd())
	cmd.AddCommand(ciCancelCmd())
	cmd.AddCommand(ciArtifactsCmd())
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
		cwd            string
		jobID          string
		event          string
		inputs         []string
		projectID      string
		requireMicroVM bool
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
			if strings.TrimSpace(projectID) != "" {
				return runRemoteCIJob(cmd.Context(), repoRoot, strings.TrimSpace(projectID), args[0], strings.TrimSpace(jobID), strings.TrimSpace(event), parseCLIInputs(inputs))
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

			runner, err := ci.NewWorkflowRunner(ci.RunnerSelection{RequireMicroVM: requireMicroVM})
			if err != nil {
				return err
			}
			fmt.Printf("Execution backend: %s\n", runner.Backend())
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
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID for remote execution via the Kindling API")
	cmd.Flags().BoolVar(&requireMicroVM, "require-microvm", false, "Require a microVM-backed local runner instead of falling back to host execution")
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

func ciGetCmd() *cobra.Command {
	var jobID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Fetch a CI job from the Kindling API",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCIJobID(jobID)
			if err != nil {
				return err
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/ci/jobs/"+id, nil, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "CI job UUID")
	return cmd
}

func ciLogsCmd() *cobra.Command {
	var jobID string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print CI job logs from the Kindling API",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCIJobID(jobID)
			if err != nil {
				return err
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/ci/jobs/"+id+"/logs", nil, &out); err != nil {
				return err
			}
			if remoteJSON {
				return printRemote(out)
			}
			for _, row := range out {
				fmt.Printf("[%s] %s %s\n", jsonFieldString(row, "created_at"), jsonFieldString(row, "level"), jsonFieldString(row, "message"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "CI job UUID")
	return cmd
}

func ciCancelCmd() *cobra.Command {
	var jobID string
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel a CI job via the Kindling API",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCIJobID(jobID)
			if err != nil {
				return err
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			resp, err := c.Do(cmd.Context(), http.MethodPost, "/api/ci/jobs/"+id+"/cancel", nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("cancel failed: %s", resp.Status)
			}
			fmt.Println("canceled", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "CI job UUID")
	return cmd
}

func ciArtifactsCmd() *cobra.Command {
	var jobID string
	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "List CI job artifacts from the Kindling API",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseCIJobID(jobID)
			if err != nil {
				return err
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/ci/jobs/"+id+"/artifacts", nil, &out); err != nil {
				return err
			}
			if remoteJSON {
				return printRemote(out)
			}
			for _, row := range out {
				fmt.Printf("%s\t%s\n", jsonFieldString(row, "name"), jsonFieldString(row, "path"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "CI job UUID")
	return cmd
}

func parseCIJobID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", fmt.Errorf("--job is required")
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("invalid ci job id: %w", err)
	}
	return id, nil
}

func runRemoteCIJob(ctx context.Context, repoRoot, projectID, workflowRef, jobID, event string, inputs map[string]string) error {
	pid, err := resolveProjectFlag(projectID)
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	if _, err := uuid.Parse(pid); err != nil {
		return fmt.Errorf("invalid project id: %w", err)
	}
	archiveBase64, err := ci.SnapshotWorkspaceBase64(repoRoot)
	if err != nil {
		return fmt.Errorf("snapshot workspace: %w", err)
	}
	c, err := mustRemoteClient()
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	var out map[string]any
	if err := c.DoJSON(ctx, http.MethodPost, "/api/projects/"+pid+"/ci/jobs", map[string]any{
		"workflow":        workflowRef,
		"job":             jobID,
		"event":           event,
		"inputs":          inputs,
		"archive_base64":  archiveBase64,
		"require_microvm": true,
	}, &out); err != nil {
		return fmt.Errorf("create ci job: %w", err)
	}
	id := jsonFieldString(out, "id")
	fmt.Printf("CI job created: %s\n", id)
	return followRemoteCIJob(ctx, c, id)
}

func followRemoteCIJob(ctx context.Context, c *cli.Client, jobID string) error {
	seenLogs := 0
	shownBackend := ""
	for {
		var logs []map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/ci/jobs/"+jobID+"/logs", nil, &logs); err == nil {
			for _, row := range logs[seenLogs:] {
				fmt.Printf("[%s] %s %s\n", jsonFieldString(row, "created_at"), jsonFieldString(row, "level"), jsonFieldString(row, "message"))
			}
			seenLogs = len(logs)
		}
		var job map[string]any
		if err := c.DoJSON(ctx, http.MethodGet, "/api/ci/jobs/"+jobID, nil, &job); err != nil {
			return err
		}
		status := jsonFieldString(job, "status")
		backend := jsonFieldString(job, "execution_backend")
		if backend != "" && backend != shownBackend {
			fmt.Printf("Execution backend: %s\n", backend)
			shownBackend = backend
		}
		switch status {
		case "successful":
			return nil
		case "failed", "canceled":
			return fmt.Errorf("ci job %s", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}
