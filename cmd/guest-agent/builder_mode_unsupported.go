//go:build !linux

package main

import "fmt"

// runBuilderMode exists on Linux only; host-side `go build` uses this stub.
func runBuilderMode(_ *ConfigResponse) error {
	return fmt.Errorf("guest-agent builder mode is only available in the Linux microVM (build with GOOS=linux)")
}
