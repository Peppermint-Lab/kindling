//go:build linux

package wgmesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// RunReconcileLoop keeps WireGuard peers in sync with the servers table.
func RunReconcileLoop(ctx context.Context, q *queries.Queries, serverID uuid.UUID, priv wgtypes.Key) {
	if err := Reconcile(ctx, q, serverID, priv); err != nil && ctx.Err() == nil {
		slog.Warn("wireguard reconcile failed", "error", err)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := Reconcile(ctx, q, serverID, priv); err != nil && ctx.Err() == nil {
			slog.Warn("wireguard reconcile failed", "error", err)
		}
	}
}

// Reconcile applies local wg0 address and full peer set from PostgreSQL.
func Reconcile(ctx context.Context, q *queries.Queries, serverID uuid.UUID, priv wgtypes.Key) error {
	if !Enabled() {
		return nil
	}
	listenPort, err := ListenPort()
	if err != nil {
		return err
	}
	link, err := ensureWGLink()
	if err != nil {
		return err
	}
	myWG := OverlayIP(serverID)
	if err := assignOverlayAddress(link, myWG); err != nil {
		return err
	}
	rows, err := q.ServerFindAll(ctx)
	if err != nil {
		return err
	}
	peers, err := buildPeerConfigs(rows, serverID, priv.PublicKey())
	if err != nil {
		return err
	}
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl: %w", err)
	}
	defer client.Close()

	privCopy := priv
	cfg := wgtypes.Config{
		PrivateKey:   &privCopy,
		ListenPort:   &listenPort,
		ReplacePeers: true,
		Peers:        peers,
	}
	if err := client.ConfigureDevice(IfaceName, cfg); err != nil {
		return fmt.Errorf("configure %s: %w", IfaceName, err)
	}
	return nil
}

func ensureWGLink() (netlink.Link, error) {
	l, err := netlink.LinkByName(IfaceName)
	if err == nil {
		return l, netlink.LinkSetUp(l)
	}
	wg := &netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: IfaceName}}
	if err := netlink.LinkAdd(wg); err != nil {
		return nil, fmt.Errorf("add wireguard link %s: %w", IfaceName, err)
	}
	l, err = netlink.LinkByName(IfaceName)
	if err != nil {
		return nil, err
	}
	return l, netlink.LinkSetUp(l)
}

func assignOverlayAddress(link netlink.Link, ip netip.Addr) error {
	prefix := netip.PrefixFrom(ip, 32)
	addr, err := netlink.ParseAddr(prefix.String())
	if err != nil {
		return err
	}
	// Replace avoids duplicate errors on re-run.
	_ = netlink.AddrReplace(link, addr)
	return nil
}

func prefixToIPNet(pref netip.Prefix) net.IPNet {
	masked := pref.Masked()
	return net.IPNet{
		IP:   masked.Addr().AsSlice(),
		Mask: net.CIDRMask(masked.Bits(), masked.Addr().BitLen()),
	}
}

func buildPeerConfigs(rows []queries.Server, self uuid.UUID, selfPub wgtypes.Key) ([]wgtypes.PeerConfig, error) {
	byKey := make(map[wgtypes.Key]*wgtypes.PeerConfig)

	merge := func(pk wgtypes.Key, ep *net.UDPAddr, allowed ...netip.Prefix) {
		p := byKey[pk]
		if p == nil {
			ka := 25 * time.Second
			p = &wgtypes.PeerConfig{
				PublicKey:                   pk,
				Endpoint:                    ep,
				PersistentKeepaliveInterval: &ka,
			}
			byKey[pk] = p
		}
		if ep != nil {
			p.Endpoint = ep
		}
		seen := make(map[string]struct{})
		for _, x := range p.AllowedIPs {
			seen[x.String()] = struct{}{}
		}
		for _, pref := range allowed {
			if !pref.IsValid() {
				continue
			}
			k := pref.String()
			if _, ok := seen[k]; ok {
				continue
			}
			p.AllowedIPs = append(p.AllowedIPs, prefixToIPNet(pref))
			seen[k] = struct{}{}
		}
	}

	if err := addCoordinationPeer(rows, merge); err != nil {
		return nil, err
	}

	for _, row := range rows {
		id := pguuid.FromPgtype(row.ID)
		if id == uuid.Nil || id == self {
			continue
		}
		keyStr := strings.TrimSpace(row.WireguardPublicKey)
		epStr := strings.TrimSpace(row.WireguardEndpoint)
		if keyStr == "" || epStr == "" {
			continue
		}
		pk, err := wgtypes.ParseKey(keyStr)
		if err != nil {
			return nil, fmt.Errorf("parse wireguard public key for server %s: %w", id, err)
		}
		if pk == selfPub {
			continue
		}
		ep, err := net.ResolveUDPAddr("udp", epStr)
		if err != nil {
			return nil, fmt.Errorf("parse wireguard endpoint %q for server %s: %w", epStr, id, err)
		}
		var allowed []netip.Prefix
		if row.WireguardIp.IsValid() && !row.WireguardIp.IsUnspecified() {
			allowed = append(allowed, netip.PrefixFrom(row.WireguardIp, 32))
		}
		if row.IpRange.IsValid() {
			allowed = append(allowed, row.IpRange)
		}
		if len(allowed) == 0 {
			continue
		}
		merge(pk, ep, allowed...)
	}

	out := make([]wgtypes.PeerConfig, 0, len(byKey))
	for _, pc := range byKey {
		if len(pc.AllowedIPs) == 0 {
			continue
		}
		out = append(out, *pc)
	}
	return out, nil
}

func addCoordinationPeer(rows []queries.Server, merge func(wgtypes.Key, *net.UDPAddr, ...netip.Prefix)) error {
	pubStr := strings.TrimSpace(os.Getenv("KINDLING_COORDINATION_SERVER_PUBKEY"))
	if pubStr == "" {
		return nil
	}
	epStr := strings.TrimSpace(os.Getenv("KINDLING_COORDINATION_SERVER_ENDPOINT"))
	if epStr == "" {
		return errors.New("KINDLING_COORDINATION_SERVER_PUBKEY set but KINDLING_COORDINATION_SERVER_ENDPOINT is empty")
	}
	ipStr := strings.TrimSpace(os.Getenv("KINDLING_COORDINATION_SERVER_WG_IP"))
	if ipStr == "" {
		return errors.New("KINDLING_COORDINATION_SERVER_PUBKEY set but KINDLING_COORDINATION_SERVER_WG_IP is empty")
	}
	pk, err := wgtypes.ParseKey(pubStr)
	if err != nil {
		return fmt.Errorf("KINDLING_COORDINATION_SERVER_PUBKEY: %w", err)
	}
	ep, err := net.ResolveUDPAddr("udp", epStr)
	if err != nil {
		return fmt.Errorf("KINDLING_COORDINATION_SERVER_ENDPOINT: %w", err)
	}
	wgIP, err := netip.ParseAddr(ipStr)
	if err != nil {
		return fmt.Errorf("KINDLING_COORDINATION_SERVER_WG_IP: %w", err)
	}
	var vmCidr netip.Prefix
	for _, row := range rows {
		keyStr := strings.TrimSpace(row.WireguardPublicKey)
		if keyStr == "" {
			continue
		}
		rowKey, err := wgtypes.ParseKey(keyStr)
		if err != nil {
			continue
		}
		if rowKey != pk {
			continue
		}
		if row.IpRange.IsValid() {
			vmCidr = row.IpRange
		}
		break
	}
	if vmCidr.IsValid() {
		merge(pk, ep, netip.PrefixFrom(wgIP, 32), vmCidr)
	} else {
		merge(pk, ep, netip.PrefixFrom(wgIP, 32))
	}
	return nil
}
