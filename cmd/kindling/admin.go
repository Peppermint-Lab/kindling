package main

import "github.com/spf13/cobra"

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Break-glass and host-local operations (direct Postgres, superuser, import-env)",
		Long: `Commands that require database access or server-local credentials.

For normal remote use, prefer top-level commands (e.g. kindling project, kindling deploy create)
with API keys or a saved session in ~/.kindling/cli-config.json.`,
	}
	cmd.AddCommand(adminProjectCmd())
	cmd.AddCommand(adminDeployCmd())
	cmd.AddCommand(adminLogsCmd())
	cmd.AddCommand(adminConfigCmd())
	cmd.AddCommand(adminAuthCmd())
	return cmd
}
