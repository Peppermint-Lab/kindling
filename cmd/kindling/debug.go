package main

import (
	"fmt"

	"github.com/kindlingvm/kindling/internal/builder"
	"github.com/spf13/cobra"
)

func debugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "debug",
		Short:  "Development and diagnostic commands",
		Hidden: true,
	}
	cmd.AddCommand(debugBuilderVMSmokeCmd())
	return cmd
}

func debugBuilderVMSmokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "builder-vm-smoke",
		Short: "Boot the Apple VZ builder VM and run `buildah version` in the guest",
		Long: `Requires ~/.kindling/vmlinuz.bin, initramfs.cpio.gz, and builder-rootfs with Linux buildah
(see CLAUDE.md). On macOS the kindling binary must be signed with the Virtualization entitlement—use "make build" not raw "go build".`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := builder.SmokeTestAppleBuilderVM(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("builder-vm-smoke: ok")
			return nil
		},
	}
}
