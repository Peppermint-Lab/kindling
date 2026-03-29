//go:build darwin

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func cliLocalBoxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "box",
		Short: "Manage the persistent box Linux VM",
	}
	cmd.AddCommand(cliLocalBoxStartCmd())
	cmd.AddCommand(cliLocalBoxStopCmd())
	cmd.AddCommand(cliLocalBoxStatusCmd())
	cmd.AddCommand(cliLocalBoxShellCmd())
	return cmd
}

func cliLocalBoxStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start (or resume) the box VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req map[string]any
			if err := api.doContext(cmd.Context(), "POST", "/box.start", req, &req); err != nil {
				return err
			}
			fmt.Printf("box started: id=%s host_port=%v\n", req["id"], req["host_port"])
			return nil
		},
	}
}

func cliLocalBoxStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the box VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			return api.doContext(cmd.Context(), "POST", "/box.stop", nil, nil)
		},
	}
}

func cliLocalBoxStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show box VM status",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			var req map[string]any
			if err := api.doContext(cmd.Context(), "GET", "/box.status", nil, &req); err != nil {
				return err
			}
			fmt.Printf("id:         %s\n", req["id"])
			fmt.Printf("name:       %s\n", req["name"])
			fmt.Printf("status:     %s\n", req["status"])
			fmt.Printf("host_group: %s\n", req["host_group"])
			if hp, ok := req["host_port"].(float64); ok && int(hp) > 0 {
				fmt.Printf("host_port:  %.0f\n", hp)
			}
			return nil
		},
	}
}

func cliLocalBoxShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive shell in the box VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			// Get box id first.
			var box map[string]any
			if err := api.doContext(cmd.Context(), "GET", "/box.status", nil, &box); err != nil {
				return fmt.Errorf("box not found: %w", err)
			}
			id, _ := box["id"].(string)
			if id == "" {
				return fmt.Errorf("box not configured")
			}
			// Shell is an interactive exec.
			var req = map[string]any{"id": id, "argv": []string{"sh"}, "cwd": "/app", "env": []string{}}
			var out struct {
				ExitCode int    `json:"exit_code"`
				Output   string `json:"output"`
			}
			if err := api.doContext(cmd.Context(), "POST", "/vm.shell", req, &out); err != nil {
				return err
			}
			if out.ExitCode != 0 {
				return fmt.Errorf("shell exited with code %d", out.ExitCode)
			}
			return nil
		},
	}
}
