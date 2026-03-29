//go:build darwin

package main

import (
	"os"

	"github.com/spf13/cobra"
)

func cliLocalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Manage local Linux VMs on macOS via the kindling-mac daemon",
		Long: `Connect to the kindling-mac daemon to manage local Linux microVMs.

If the daemon is not already running, local commands will try to start it automatically.
You can also start it yourself with: kindling-mac

The daemon manages two kinds of VMs:
  box   — a persistent Linux VM for day-to-day development (like WSL)
  temp  — ephemeral disposable VMs for agents and CI workloads
`,
	}
	cmd.AddCommand(cliLocalStatusCmd())
	cmd.AddCommand(cliLocalBoxCmd())
	cmd.AddCommand(cliLocalTempCmd())
	cmd.AddCommand(cliLocalTemplateCmd())
	cmd.AddCommand(cliLocalVMListCmd())
	return cmd
}

func localAPI() (*localAPIClient, error) {
	socketPath := os.Getenv("KINDLING_MAC_SOCKET")
	if socketPath == "" {
		home, _ := os.UserHomeDir()
		socketPath = home + "/.kindling-mac/kindling-mac.sock"
	}
	return newLocalAPI(socketPath), nil
}

func cliLocalStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and VM status",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			return api.Status(cmd.Context())
		},
	}
}

func cliLocalVMListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all local VMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := localAPI()
			if err != nil {
				return err
			}
			return api.ListVMs(cmd.Context())
		},
	}
}
