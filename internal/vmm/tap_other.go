//go:build !linux

package vmm

import (
	"fmt"
	"net/netip"
)

func createTAP(tapName string, hostIP netip.Addr) error {
	return fmt.Errorf("TAP networking requires Linux")
}

func removeTAP(tapName string) {
	// no-op on non-Linux
}
