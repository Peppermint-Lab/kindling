package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "kindling",
		Short: "Self-hosted PaaS on Cloud Hypervisor microVMs",
		Long: `Kindling control CLI.

Remote control (API): configure ~/.kindling/cli-config.json or use --api-url / KINDLING_API_URL,
then authenticate with kindling auth login or kindling auth api-key create / --api-key.

Break-glass: kindling admin ... for direct PostgreSQL operations.`,
	}

	remotePersistentFlags(root)

	root.AddCommand(serveCmd())
	root.AddCommand(cliAuthCmd())
	root.AddCommand(cliContextCmd())
	root.AddCommand(cliLinkCmd())
	root.AddCommand(cliStatusCmd())
	root.AddCommand(cliProjectRemoteCmd())
	root.AddCommand(cliDeployRemoteCmd())
	root.AddCommand(cliLogsRemoteCmd())
	root.AddCommand(cliDomainRemoteCmd())
	root.AddCommand(adminCmd())
	root.AddCommand(debugCmd())
	root.AddCommand(chBridgeProxyCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
