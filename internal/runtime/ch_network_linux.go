//go:build linux

package runtime

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
)

func cloudHypervisorIPs(slot uint32) (string, string, error) {
	base := netip.MustParseAddr("10.0.0.0")
	host := base
	for i := uint32(0); i < slot*2; i++ {
		host = host.Next()
	}
	guest := host.Next()
	if !guest.IsValid() {
		return "", "", fmt.Errorf("invalid guest ip for slot %d", slot)
	}
	return host.String(), guest.String() + "/31", nil
}

func cloudHypervisorTapName(id uuid.UUID, slot uint32) string {
	// Linux interface names max out at 15 chars. Keep enough deployment entropy to
	// avoid collisions across process restarts while still leaving room for retries.
	return fmt.Sprintf("kch%s%x", id.String()[:8], slot&0xf)
}

func createCHTap(tapName string, hostIP netip.Addr) error {
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: tapName},
		Mode:      netlink.TUNTAP_MODE_TAP,
	}
	if err := netlink.LinkAdd(tap); err != nil {
		return fmt.Errorf("create TAP %s: %w", tapName, err)
	}
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("find TAP %s: %w", tapName, err)
	}
	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: hostIP.AsSlice(), Mask: net.CIDRMask(31, 32)},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr to %s: %w", tapName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up %s: %w", tapName, err)
	}
	return nil
}

func cloudHypervisorDiskArgs(workDisk string, vol *PersistentVolumeMount) []string {
	args := []string{"--disk", fmt.Sprintf("path=%s,direct=off", workDisk)}
	if vol != nil && strings.TrimSpace(vol.HostPath) != "" {
		args = append(args, "--disk", fmt.Sprintf("path=%s,direct=off", vol.HostPath))
	}
	return args
}

func removeCHTap(tapName string) {
	if link, err := netlink.LinkByName(tapName); err == nil {
		_ = netlink.LinkDel(link)
	}
}
