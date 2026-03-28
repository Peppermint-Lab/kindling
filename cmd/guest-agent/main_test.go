package main

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestWaitForTCPReadySucceedsWhenListenerIsUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := waitForTCPReady(ctx, ln.Addr().String()); err != nil {
		t.Fatalf("expected listener to be detected as ready: %v", err)
	}
}

func TestWaitForTCPReadyReturnsErrorWhenDeadlineExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if err := waitForTCPReady(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("expected waitForTCPReady to fail when nothing is listening")
	}
}

func TestNetworkCommandsBringUpLoopbackBeforeEth0(t *testing.T) {
	cfg := &ConfigResponse{
		IPAddr: "10.0.0.1/31",
		IPGW:   "10.0.0.0",
	}

	got := networkCommands(cfg)
	want := []commandSpec{
		{name: "ip", args: []string{"link", "set", "lo", "up"}},
		{name: "ip", args: []string{"link", "set", "eth0", "up"}},
		{name: "ip", args: []string{"addr", "add", "10.0.0.1/31", "dev", "eth0"}},
		{name: "ip", args: []string{"route", "add", "default", "via", "10.0.0.0"}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command list:\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestRenderResolvConfPrefersConfiguredDNSServers(t *testing.T) {
	cfg := &ConfigResponse{
		DNSServers: []string{"10.0.0.1", "1.1.1.1"},
	}

	got := renderResolvConf(cfg)
	want := "nameserver 10.0.0.1\nnameserver 1.1.1.1\n"
	if got != want {
		t.Fatalf("unexpected resolv.conf contents:\nwant: %q\ngot:  %q", want, got)
	}
}
