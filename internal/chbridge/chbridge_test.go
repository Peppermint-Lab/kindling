package chbridge

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRelayKeepsResponseFlowingAfterClientHalfClose(t *testing.T) {
	guestLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen guest: %v", err)
	}
	defer guestLn.Close()

	guestDone := make(chan error, 1)
	go func() {
		conn, err := guestLn.Accept()
		if err != nil {
			guestDone <- err
			return
		}
		defer conn.Close()

		req, err := io.ReadAll(conn)
		if err != nil {
			guestDone <- err
			return
		}
		if !strings.Contains(string(req), "GET / HTTP/1.1") {
			guestDone <- io.ErrUnexpectedEOF
			return
		}

		time.Sleep(25 * time.Millisecond)
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		guestDone <- err
	}()

	prevDial := dialGuestOverUDS
	dialGuestOverUDS = func(_ string, _ uint32) (net.Conn, error) {
		return net.Dial("tcp", guestLn.Addr().String())
	}
	defer func() { dialGuestOverUDS = prevDial }()

	bridgeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen bridge: %v", err)
	}
	defer bridgeLn.Close()

	go func() {
		conn, err := bridgeLn.Accept()
		if err != nil {
			return
		}
		Relay(conn, "ignored", 1025)
	}()

	clientConn, err := net.Dial("tcp", bridgeLn.Addr().String())
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
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
	if err := <-guestDone; err != nil {
		t.Fatalf("guest write failed: %v", err)
	}
}
