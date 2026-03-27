package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func cliProjectRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage projects via the control-plane API",
	}
	cmd.AddCommand(cliProjectListCmd())
	cmd.AddCommand(cliProjectGetCmd())
	cmd.AddCommand(cliProjectCreateCmd())
	cmd.AddCommand(cliProjectDeleteCmd())
	return cmd
}

func cliProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List projects in the current organization",
		Example: `  kindling project list
  kindling project list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/projects", nil, &out); err != nil {
				return fmt.Errorf("list projects: %w", err)
			}
			if !remoteJSON {
				for _, p := range out {
					fmt.Printf("%s  %-20s  %s\n", jsonFieldString(p, "id"), jsonFieldString(p, "name"), jsonFieldString(p, "github_repository"))
				}
				return nil
			}
			return printRemote(out)
		},
	}
}

func cliProjectGetCmd() *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get one project by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid project id: use --project or kindling link --project <uuid>\n%w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/projects/"+id, nil, &out); err != nil {
				return fmt.Errorf("fetch project: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked default)")
	return cmd
}

func cliProjectCreateCmd() *cobra.Command {
	var name, repo, dockerfile, rootDir string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a project (requires org admin)",
		Example: `  kindling project create --name myapp --repo owner/myapp
  kindling project create --name myapp --repo owner/myapp --dockerfile Dockerfile --root-directory /`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required\n  kindling project create --name myapp --repo owner/repo")
			}
			body := map[string]any{
				"name":             strings.TrimSpace(name),
				"github_repository": strings.TrimSpace(repo),
				"dockerfile_path":   strings.TrimSpace(dockerfile),
				"root_directory":    strings.TrimSpace(rootDir),
			}
			if body["dockerfile_path"] == "" {
				body["dockerfile_path"] = "Dockerfile"
			}
			if body["root_directory"] == "" {
				body["root_directory"] = "/"
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/projects", body, &out); err != nil {
				return fmt.Errorf("create project: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Project name (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo owner/name (optional)")
	cmd.Flags().StringVar(&dockerfile, "dockerfile", "Dockerfile", "Path to Dockerfile relative to root")
	cmd.Flags().StringVar(&rootDir, "root-directory", "/", "Build context root in repo")
	return cmd
}

func cliProjectDeleteCmd() *cobra.Command {
	var projectID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a project (requires org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid project id: %w", err)
			}
			if !yes {
				return fmt.Errorf("refusing to delete without --yes (destructive)\n  kindling project delete --project %s --yes", id)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			resp, err := c.Do(cmd.Context(), http.MethodDelete, "/api/projects/"+id, nil)
			if err != nil {
				return fmt.Errorf("delete project request: %w", err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("delete failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
			}
			printRemoteMessage("project deleted: " + id)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (required if not linked)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm destructive delete")
	return cmd
}

func resolveProjectFlag(projectFlag string) (string, error) {
	s := strings.TrimSpace(projectFlag)
	if s != "" {
		return s, nil
	}
	return linkedProjectID()
}

func jsonFieldString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
