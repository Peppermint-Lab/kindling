package vmm

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

func createTAP(tapName string, hostIP netip.Addr) error {
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
		IPNet: &net.IPNet{
			IP:   hostIP.AsSlice(),
			Mask: net.CIDRMask(31, 32),
		},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("add addr to %s: %w", tapName, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up %s: %w", tapName, err)
	}

	return nil
}

func removeTAP(tapName string) {
	if link, err := netlink.LinkByName(tapName); err == nil {
		netlink.LinkDel(link)
	}
}
