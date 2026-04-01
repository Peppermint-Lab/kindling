package main

import (
	"context"
	"io"
	"net"
	"reflect"
	"strings"
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

func TestIsRemoteVMGuestDetectsEnabledMarker(t *testing.T) {
	if !isRemoteVMGuest([]string{"FOO=bar", "KINDLING_REMOTE_VM=1"}) {
		t.Fatal("expected KINDLING_REMOTE_VM=1 to enable remote VM behavior")
	}
	if !isRemoteVMGuest([]string{"KINDLING_REMOTE_VM=true"}) {
		t.Fatal("expected KINDLING_REMOTE_VM=true to enable remote VM behavior")
	}
	if isRemoteVMGuest([]string{"KINDLING_REMOTE_VM=0"}) {
		t.Fatal("expected KINDLING_REMOTE_VM=0 to disable remote VM behavior")
	}
}

func TestShouldKeepGuestReadyWithoutAppForRemoteVM(t *testing.T) {
	cfg := &ConfigResponse{Env: []string{"KINDLING_REMOTE_VM=1"}}
	if !shouldKeepGuestReadyWithoutApp(cfg) {
		t.Fatal("expected remote VM guest to become ready without an app")
	}
	if !shouldStartHostBridgeWithoutApp(cfg) {
		t.Fatal("expected remote VM guest to start the TCP bridge without an app")
	}
}

func TestBridgeGuestConnToAppKeepsResponseFlowingAfterClientHalfClose(t *testing.T) {
	appLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen app: %v", err)
	}
	defer appLn.Close()

	appDone := make(chan error, 1)
	go func() {
		conn, err := appLn.Accept()
		if err != nil {
			appDone <- err
			return
		}
		defer conn.Close()

		req, err := io.ReadAll(conn)
		if err != nil {
			appDone <- err
			return
		}
		if !strings.Contains(string(req), "GET / HTTP/1.1") {
			appDone <- io.ErrUnexpectedEOF
			return
		}

		time.Sleep(25 * time.Millisecond)
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		appDone <- err
	}()

	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen guest: %v", err)
	}
	defer guestLn.Close()

	go func() {
		conn, err := guestLn.Accept()
		if err != nil {
			return
		}
		bridgeGuestConnToApp(conn, appLn.Addr().String())
	}()

	clientConn, err := net.Dial("tcp", guestLn.Addr().String())
	if err != nil {
		t.Fatalf("dial guest bridge: %v", err)
	}
	defer clientConn.Close()

	tcpClient, ok := clientConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("expected *net.TCPConn, got %T", clientConn)
	}
	if _, err := io.WriteString(clientConn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := tcpClient.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

	resp, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.Contains(string(resp), "200 OK") || !strings.HasSuffix(string(resp), "ok") {
		t.Fatalf("response = %q, want full HTTP 200 response", string(resp))
	}
	if err := <-appDone; err != nil {
		t.Fatalf("app write failed: %v", err)
	}
}
