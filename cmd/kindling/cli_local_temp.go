//go:build darwin

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func cliLocalTempCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "temp",
		Short: "Manage ephemeral temp VMs (disposable, for agents and CI)",
	}
	cmd.AddCommand(cliLocalTempCreateCmd())
	cmd.AddCommand(cliLocalTempListCmd())
	cmd.AddCommand(cliLocalTempDeleteCmd())
	cmd.AddCommand(cliLocalTempExecCmd())
	return cmd
}

func cliLocalTempCreateCmd() *cobra.Command {
	var templateFlag string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create and start a new ephemeral temp VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req = map[string]string{"template": templateFlag}
			var out map[string]any
			if err := api.doContext(cmd.Context(), "POST", "/temp.create", req, &out); err != nil {
				return err
			}
			fmt.Printf("temp created: id=%s name=%s host_port=%v\n", out["id"], out["name"], out["host_port"])
			return nil
		},
	}
	cmd.Flags().StringVar(&templateFlag, "template", "", "Start from a named template (fast clone)")
	return cmd
}

func cliLocalTempListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all temp VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var out []map[string]any
			if err := api.doContext(cmd.Context(), "GET", "/temp.list", nil, &out); err != nil {
				return err
			}
			if len(out) == 0 {
				fmt.Println("no temp VMs")
				return nil
			}
			fmt.Printf("%-36s  %-12s  %-8s  %s\n", "ID", "HOST GROUP", "STATUS", "NAME")
			for _, vm := range out {
				fmt.Printf("%-36s  %-12s  %-8s  %s\n", vm["id"], vm["host_group"], vm["status"], vm["name"])
			}
			return nil
		},
	}
}

func cliLocalTempDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an temp VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req = map[string]string{"id": args[0]}
			return api.doContext(cmd.Context(), "POST", "/temp.delete", req, nil)
		},
	}
}

func cliLocalTempExecCmd() *cobra.Command {
	var cwdFlag string
	var envFlags []string

	cmd := &cobra.Command{
		Use:   "exec <id> -- <command>",
		Short: "Execute a command inside an temp VM",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("usage: temp exec <id> -- <command>")
			}
			api, err := localAPI()
			if err != nil {
				return err
			}
			id := args[0]
			argv := args[1:]
			var req = map[string]any{"id": id, "argv": argv, "cwd": cwdFlag, "env": envFlags}
			var out struct {
				ExitCode int    `json:"exit_code"`
				Output   string `json:"output"`
			}
			if err := api.doContext(cmd.Context(), "POST", "/vm.exec", req, &out); err != nil {
				return err
			}
			fmt.Print(out.Output)
			if out.ExitCode != 0 {
				return fmt.Errorf("command exited with code %d", out.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cwdFlag, "cwd", "/", "Working directory")
	cmd.Flags().StringArrayVar(&envFlags, "env", nil, "Environment variables (KEY=value)")
	return cmd
}
