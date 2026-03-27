package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kindlingvm/kindling/internal/chbridge"
	"github.com/spf13/cobra"
)

func chBridgeProxyCmd() *cobra.Command {
	var (
		listenAddr string
		vsockUDS   string
		guestPort  uint32
	)

	cmd := &cobra.Command{
		Use:    "ch-bridge-proxy",
		Short:  "Run the Cloud Hypervisor host TCP to guest vsock bridge",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listenAddr == "" {
				return fmt.Errorf("--listen is required")
			}
			if vsockUDS == "" {
				return fmt.Errorf("--vsock is required")
			}
			if guestPort == 0 {
				return fmt.Errorf("--guest-port must be greater than zero")
			}

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return chbridge.ListenAndServe(ctx, listenAddr, vsockUDS, guestPort)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", "", "TCP listen address")
	cmd.Flags().StringVar(&vsockUDS, "vsock", "", "Cloud Hypervisor vsock Unix socket path")
	cmd.Flags().Uint32Var(&guestPort, "guest-port", 0, "Guest vsock port")
	return cmd
}
