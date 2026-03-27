package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func cliLogsRemoteCmd() *cobra.Command {
	var deploymentID string
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print build logs for a deployment",
		Long:  "Fetches GET /api/deployments/{id}/logs. With --follow, streams GET /api/deployments/{id}/stream (SSE) to stdout.",
		Example: `  kindling logs --deployment <uuid>
  kindling logs --deployment <uuid> --follow`,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(deploymentID)
			if id == "" {
				return fmt.Errorf("--deployment is required\n  kindling logs --deployment <uuid>")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid deployment id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			if follow {
				resp, err := c.DoStream(cmd.Context(), http.MethodGet, "/api/deployments/"+id+"/stream")
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					b, _ := io.ReadAll(resp.Body)
					return fmt.Errorf("stream failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
				}
				_, err = io.Copy(os.Stdout, resp.Body)
				return err
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/deployments/"+id+"/logs", nil, &out); err != nil {
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
	cmd.Flags().StringVar(&deploymentID, "deployment", "", "Deployment UUID (required)")
	cmd.Flags().BoolVar(&follow, "follow", false, "Stream deployment + logs via SSE")
	return cmd
}
