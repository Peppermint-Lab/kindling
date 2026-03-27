package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

func cliLinkCmd() *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Remember a default project id for deploy/logs shortcuts",
		Example: `  kindling link --project 550e8400-e29b-41d4-a716-446655440000
  kindling link --project ""   # clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, fc, err := loadFileConfig()
			if err != nil {
				return err
			}
			pid := strings.TrimSpace(projectID)
			if pid != "" {
				if _, err := uuid.Parse(pid); err != nil {
					return fmt.Errorf("invalid project id: %w", err)
				}
			}
			fc.LinkedProjectID = pid
			if err := cli.SaveFileConfig(path, fc); err != nil {
				return err
			}
			if pid == "" {
				printRemoteMessage("link: cleared linked project")
			} else {
				printRemoteMessage("link: default project " + pid)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (empty to clear)")
	return cmd
}

func cliStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show API reachability and session (GET /api/auth/session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("client: %w\n  kindling context use --profile default --api-url http://127.0.0.1:8080", err)
			}
			var out map[string]any
			// session endpoint is public; still send auth if configured
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/auth/session", nil, &out); err != nil {
				return err
			}
			if !remoteJSON {
				fmt.Printf("api_url: %s\n", c.BaseURL)
				if auth, ok := out["authenticated"].(bool); ok {
					fmt.Printf("authenticated: %v\n", auth)
				}
				return nil
			}
			out["api_url"] = c.BaseURL
			return printRemote(out)
		},
	}
}

func linkedProjectID() (string, error) {
	_, fc, err := loadFileConfig()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(fc.LinkedProjectID), nil
}
