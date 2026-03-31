package chbridge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ListenAndServe listens on addr and forwards accepted TCP connections to the
// Cloud Hypervisor guest bridge vsock exposed via the Unix domain socket.
func ListenAndServe(ctx context.Context, addr, vsockUDS string, guestPort uint32) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept connection: %w", err)
			}
		}
		go Relay(conn, vsockUDS, guestPort)
	}
}

// Relay forwards one inbound TCP connection to the guest app over virtio-vsock.
func Relay(client net.Conn, vsockUDS string, guestPort uint32) {
	defer client.Close()

	back, err := DialGuestOverUDS(vsockUDS, guestPort)
	if err != nil {
		return
	}
	defer back.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = ioCopyClose(back, client)
	}()
	go func() {
		defer wg.Done()
		_, _ = ioCopyClose(client, back)
	}()
	wg.Wait()
}

// DialGuestOverUDS dials the Cloud Hypervisor vsock bridge and returns a net.Conn
// positioned after the Firecracker-style "OK" handshake line.
func DialGuestOverUDS(vsockUDS string, port uint32) (net.Conn, error) {
	c, err := net.Dial("unix", vsockUDS)
	if err != nil {
		return nil, err
	}
	handshakeDeadline := time.Now().Add(15 * time.Second)
	if err := c.SetDeadline(handshakeDeadline); err != nil {
		_ = c.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("vsock connect ack: %w", err)
	}
	if len(line) < 3 || line[0] != 'O' || line[1] != 'K' {
		_ = c.Close()
		return nil, fmt.Errorf("vsock connect ack: got %q", strings.TrimSpace(line))
	}
	_ = c.SetDeadline(time.Time{})
	return &bridgedConn{Conn: c, br: br}, nil
}

type bridgedConn struct {
	net.Conn
	br *bufio.Reader
}

func (v *bridgedConn) Read(b []byte) (int, error) { return v.br.Read(b) }

func ioCopyClose(dst io.Writer, src interface {
	io.Reader
	Close() error
}) (int64, error) {
	n, err := io.Copy(dst, src)
	_ = src.Close()
	return n, err
}
