package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func cliDomainRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage custom domains for a project",
	}
	cmd.AddCommand(cliDomainListCmd())
	cmd.AddCommand(cliDomainAddCmd())
	cmd.AddCommand(cliDomainVerifyCmd())
	cmd.AddCommand(cliDomainDeleteCmd())
	return cmd
}

func cliDomainListCmd() *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List domains for a project",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(pid); err != nil {
				return fmt.Errorf("invalid project id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out []map[string]any
			path := fmt.Sprintf("/api/projects/%s/domains", pid)
			if err := c.DoJSON(cmd.Context(), http.MethodGet, path, nil, &out); err != nil {
				return fmt.Errorf("list domains: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked)")
	return cmd
}

func cliDomainAddCmd() *cobra.Command {
	var projectID, name string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a custom domain (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(pid); err != nil {
				return fmt.Errorf("invalid project id: %w", err)
			}
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required (FQDN)\n  kindling domain add --project <uuid> --name app.example.com")
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			path := fmt.Sprintf("/api/projects/%s/domains", pid)
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, path, map[string]string{"domain_name": strings.TrimSpace(name)}, &out); err != nil {
				return fmt.Errorf("add domain: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked)")
	cmd.Flags().StringVar(&name, "name", "", "Domain name (required)")
	return cmd
}

func cliDomainVerifyCmd() *cobra.Command {
	var projectID, domainID string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify DNS for a domain (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(pid); err != nil {
				return fmt.Errorf("invalid project id: %w", err)
			}
			did := strings.TrimSpace(domainID)
			if did == "" {
				return fmt.Errorf("--domain-id is required (from domain list)")
			}
			if _, err := uuid.Parse(did); err != nil {
				return fmt.Errorf("invalid domain id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			path := fmt.Sprintf("/api/projects/%s/domains/%s/verify", pid, did)
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, path, nil, &out); err != nil {
				return fmt.Errorf("verify domain: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked)")
	cmd.Flags().StringVar(&domainID, "domain-id", "", "Domain row UUID")
	return cmd
}

func cliDomainDeleteCmd() *cobra.Command {
	var projectID, domainID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Remove a custom domain (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := resolveProjectFlag(projectID)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			if _, err := uuid.Parse(pid); err != nil {
				return fmt.Errorf("invalid project id: %w", err)
			}
			did := strings.TrimSpace(domainID)
			if did == "" {
				return fmt.Errorf("--domain-id is required")
			}
			if !yes {
				return fmt.Errorf("refusing without --yes\n  kindling domain delete --project %s --domain-id %s --yes", pid, did)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			path := fmt.Sprintf("/api/projects/%s/domains/%s", pid, did)
			resp, err := c.Do(cmd.Context(), http.MethodDelete, path, nil)
			if err != nil {
				return fmt.Errorf("delete domain request: %w", err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("delete failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
			}
			printRemoteMessage("domain deleted: " + did)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "Project UUID (or linked)")
	cmd.Flags().StringVar(&domainID, "domain-id", "", "Domain row UUID")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm delete")
	return cmd
}
