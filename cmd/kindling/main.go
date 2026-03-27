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
	}

	root.AddCommand(serveCmd())
	root.AddCommand(projectCmd())
	root.AddCommand(deployCmd())
	root.AddCommand(logsCmd())
	root.AddCommand(configCmd())
	root.AddCommand(authCmd())
	root.AddCommand(debugCmd())
	root.AddCommand(chBridgeProxyCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
