//go:build !darwin

package main

import "github.com/spf13/cobra"

func addPlatformCommands(root *cobra.Command) {}
