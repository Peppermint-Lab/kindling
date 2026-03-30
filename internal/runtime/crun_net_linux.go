//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	crunNetworkBaseCIDR = "10.40.0.0"
	crunNetworkMaxSlots = 1 << 15
)

var crunNetworkNextSlot atomic.Uint32

type crunOCIState struct {
	PID int `json:"pid"`
}

func crunStateMainPID(containerID string) (int, error) {
	out, err := exec.Command("crun", "state", containerID).Output()
	if err != nil {
		return 0, err
	}
	var st crunOCIState
	if err := json.Unmarshal(out, &st); err != nil {
		return 0, fmt.Errorf("parse crun state: %w", err)
	}
	if st.PID <= 0 {
		return 0, errors.New("crun state: invalid pid")
	}
	return st.PID, nil
}

func waitCrunInitPID(ctx context.Context, containerID string) (int, error) {
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("waiting for crun pid %s: %w", containerID, ctx.Err())
		case <-tick.C:
			pid, err := crunStateMainPID(containerID)
			if err == nil && pid > 0 {
				return pid, nil
			}
		}
	}
}

func crunNetworkIPs(slot uint32) (netip.Addr, netip.Addr, netip.Prefix, error) {
	base := netip.MustParseAddr(crunNetworkBaseCIDR)
	host := base
	for i := uint32(0); i < slot*2; i++ {
		host = host.Next()
	}
	guest := host.Next()
	if !guest.IsValid() {
		return netip.Addr{}, netip.Addr{}, netip.Prefix{}, fmt.Errorf("invalid crun guest ip for slot %d", slot)
	}
	prefix := netip.PrefixFrom(guest, 31).Masked()
	return host, guest, prefix, nil
}

func crunNetworkCIDRInUse(prefix netip.Prefix) (bool, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("list IPv4 routes: %w", err)
	}
	for _, route := range routes {
		if route.Dst == nil || route.Dst.IP == nil {
			continue
		}
		existing, ok := netip.AddrFromSlice(route.Dst.IP)
		if !ok {
			continue
		}
		ones, bits := route.Dst.Mask.Size()
		if bits != 32 {
			continue
		}
		if existing == prefix.Addr() && ones == prefix.Bits() {
			return true, nil
		}
	}
	return false, nil
}

func reserveCrunNetworkSlot() (uint32, error) {
	start := crunNetworkNextSlot.Add(1) - 1
	for i := uint32(0); i < crunNetworkMaxSlots; i++ {
		slot := (start + i) % crunNetworkMaxSlots
		_, _, prefix, err := crunNetworkIPs(slot)
		if err != nil {
			return 0, err
		}
		inUse, err := crunNetworkCIDRInUse(prefix)
		if err != nil {
			return 0, err
		}
		if !inUse {
			crunNetworkNextSlot.Store(slot + 1)
			return slot, nil
		}
	}
	return 0, fmt.Errorf("no available crun network slots")
}

func crunHostVethName(id uuid.UUID) string {
	return fmt.Sprintf("kci%sh", id.String()[:8])
}

func crunGuestVethName(id uuid.UUID) string {
	return fmt.Sprintf("kci%sg", id.String()[:8])
}

func runInPIDNetworkNamespace(pid int, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hostNS, err := os.Open("/proc/self/ns/net")
		if err != nil {
			done <- err
			return
		}
		defer hostNS.Close()

		ctnNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
		if err != nil {
			done <- err
			return
		}
		defer ctnNS.Close()

		if err := unix.Setns(int(ctnNS.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- err
			return
		}
		runErr := fn()
		restoreErr := unix.Setns(int(hostNS.Fd()), unix.CLONE_NEWNET)
		if runErr != nil {
			done <- runErr
			return
		}
		if restoreErr != nil {
			done <- fmt.Errorf("restore host netns: %w", restoreErr)
			return
		}
		done <- nil
	}()
	return <-done
}

func configureCrunGuestVeth(pid int, ifName string, hostIP, guestIP netip.Addr) error {
	return runInPIDNetworkNamespace(pid, func() error {
		lo, err := netlink.LinkByName("lo")
		if err == nil {
			_ = netlink.LinkSetUp(lo)
		}
		link, err := netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("find guest veth %s: %w", ifName, err)
		}
		addr := &netlink.Addr{
			IPNet: &net.IPNet{IP: guestIP.AsSlice(), Mask: net.CIDRMask(31, 32)},
		}
		if err := netlink.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("add guest addr to %s: %w", ifName, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("bring up guest veth %s: %w", ifName, err)
		}
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Gw:        hostIP.AsSlice(),
		}
		if err := netlink.RouteAdd(route); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("add default route via %s: %w", hostIP, err)
		}
		return nil
	})
}

func setupCrunContainerNetworking(id uuid.UUID, pid int) (func(), error) {
	slot, err := reserveCrunNetworkSlot()
	if err != nil {
		return nil, err
	}
	hostIP, guestIP, _, err := crunNetworkIPs(slot)
	if err != nil {
		return nil, err
	}
	hostName := crunHostVethName(id)
	guestName := crunGuestVethName(id)

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: hostName},
		PeerName:  guestName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return nil, fmt.Errorf("create veth %s: %w", hostName, err)
	}
	cleanup := func() {
		if link, err := netlink.LinkByName(hostName); err == nil {
			_ = netlink.LinkDel(link)
		}
	}

	hostLink, err := netlink.LinkByName(hostName)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("find host veth %s: %w", hostName, err)
	}
	guestLink, err := netlink.LinkByName(guestName)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("find guest veth %s: %w", guestName, err)
	}
	if err := netlink.LinkSetNsPid(guestLink, pid); err != nil {
		cleanup()
		return nil, fmt.Errorf("move guest veth %s to pid %d: %w", guestName, pid, err)
	}
	addr := &netlink.Addr{
		IPNet: &net.IPNet{IP: hostIP.AsSlice(), Mask: net.CIDRMask(31, 32)},
	}
	if err := netlink.AddrAdd(hostLink, addr); err != nil && !errors.Is(err, unix.EEXIST) {
		cleanup()
		return nil, fmt.Errorf("add host addr to %s: %w", hostName, err)
	}
	if err := netlink.LinkSetUp(hostLink); err != nil {
		cleanup()
		return nil, fmt.Errorf("bring up host veth %s: %w", hostName, err)
	}
	if err := configureCrunGuestVeth(pid, guestName, hostIP, guestIP); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

// dialTCPInPIDNetworkNamespace dials address (e.g. "127.0.0.1:22") inside the
// network namespace of process pid.
func dialTCPInPIDNetworkNamespace(ctx context.Context, pid int, address string) (net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var conn net.Conn
		err := runInPIDNetworkNamespace(pid, func() error {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", address)
			if err != nil {
				return err
			}
			conn = c
			return nil
		})
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			ch <- res{err: err}
			return
		}
		ch <- res{c: conn}
	}()
	r := <-ch
	return r.c, r.err
}

// startCrunHostTCPForward listens on listenAddr (e.g. "127.0.0.1:3456") on the
// host and forwards each connection to guestAddr (e.g. "127.0.0.1:8080") inside
// pid's network namespace.
func startCrunHostTCPForward(ctx context.Context, listenAddr string, guestAddr string, pid int) (stop func(), err error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	fwdCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-fwdCtx.Done():
					return
				default:
				}
				if errors.Is(err, net.ErrClosed) {
					return
				}
				slog.Warn("crun host tcp forward accept failed", "listen_addr", listenAddr, "err", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			c := conn
			go func() {
				defer c.Close()
				down, err := dialTCPInPIDNetworkNamespace(fwdCtx, pid, guestAddr)
				if err != nil {
					return
				}
				defer down.Close()
				var copyWG sync.WaitGroup
				copyWG.Add(2)
				go func() {
					defer copyWG.Done()
					_, _ = io.Copy(down, c)
				}()
				go func() {
					defer copyWG.Done()
					_, _ = io.Copy(c, down)
				}()
				copyWG.Wait()
			}()
		}
	}()
	return func() {
		cancel()
		_ = ln.Close()
		wg.Wait()
	}, nil
}
