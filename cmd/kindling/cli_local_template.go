//go:build darwin

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func cliLocalTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage VM templates for fast temp cloning",
	}
	cmd.AddCommand(cliLocalTemplateListCmd())
	cmd.AddCommand(cliLocalTemplateCaptureCmd())
	cmd.AddCommand(cliLocalTemplateDeleteCmd())
	return cmd
}

func cliLocalTemplateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available VM templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var out []map[string]any
			if err := api.doContext(cmd.Context(), "GET", "/template.list", nil, &out); err != nil {
				return err
			}
			if len(out) == 0 {
				fmt.Println("no templates")
				return nil
			}
			fmt.Printf("%-36s  %-12s  %s\n", "ID", "HOST GROUP", "NAME")
			for _, t := range out {
				fmt.Printf("%-36s  %-12s  %s\n", t["id"], t["host_group"], t["name"])
			}
			return nil
		},
	}
}

func cliLocalTemplateCaptureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "capture <vm_id> <name>",
		Short: "Capture a stopped VM as a named template for fast cloning",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req = map[string]string{"vm_id": args[0], "name": args[1]}
			var out map[string]any
			if err := api.doContext(cmd.Context(), "POST", "/template.capture", req, &out); err != nil {
				return err
			}
			fmt.Printf("template created: id=%s name=%s\n", out["id"], out["name"])
			return nil
		},
	}
}

func cliLocalTemplateDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <template_id>",
		Short: "Delete a VM template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req = map[string]string{"id": args[0]}
			return api.doContext(cmd.Context(), "POST", "/template.delete", req, nil)
		},
	}
}
