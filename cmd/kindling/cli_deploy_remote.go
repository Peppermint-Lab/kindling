package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func cliDeployRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Create or inspect deployments via the control-plane API",
	}
	cmd.AddCommand(cliDeployCreateCmd())
	cmd.AddCommand(cliDeployGetCmd())
	cmd.AddCommand(cliDeployCancelCmd())
	return cmd
}

func cliDeployCreateCmd() *cobra.Command {
	var projectID, commit string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Trigger a deployment (org admin; POST /api/projects/{id}/deploy)",
		Example: `  kindling deploy create --project <uuid> --commit main
  kindling link --project <uuid> && kindling deploy create --commit abc123`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := resolveProjectFlag(projectID)
			if err != nil {
				return err
			}
			if _, err := uuid.Parse(pid); err != nil {
				return fmt.Errorf("invalid project id: use --project or kindling link\n%w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			co := strings.TrimSpace(commit)
			if co == "" {
				co = "main"
			}
			var out map[string]any
			path := fmt.Sprintf("/api/projects/%s/deploy", pid)
			if err := c.DoJSON(cmd.Context(), http.MethodPost, path, map[string]string{"commit": co}, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked default)")
	cmd.Flags().StringVar(&commit, "commit", "main", "Git commit SHA or branch/ref")
	return cmd
}

func cliDeployGetCmd() *cobra.Command {
	var deploymentID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get deployment details",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(deploymentID)
			if id == "" {
				return fmt.Errorf("--deployment is required\n  kindling deploy get --deployment <uuid>")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid deployment id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/deployments/"+id, nil, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&deploymentID, "deployment", "", "Deployment UUID (required)")
	return cmd
}

func cliDeployCancelCmd() *cobra.Command {
	var deploymentID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel a deployment (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(deploymentID)
			if id == "" {
				return fmt.Errorf("--deployment is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid deployment id: %w", err)
			}
			if !yes {
				return fmt.Errorf("refusing without --yes\n  kindling deploy cancel --deployment %s --yes", id)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			resp, err := c.Do(cmd.Context(), http.MethodPost, "/api/deployments/"+id+"/cancel", nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("cancel failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
			}
			printRemoteMessage("deployment cancelled: " + id)
			return nil
		},
	}
	cmd.Flags().StringVar(&deploymentID, "deployment", "", "Deployment UUID")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm cancel")
	return cmd
}
