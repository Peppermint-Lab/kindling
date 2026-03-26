package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// vsockConn wraps a raw vsock file descriptor as a net.Conn.
type vsockConn struct {
	f *os.File
}

func (c *vsockConn) Read(b []byte) (int, error)         { return c.f.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error)        { return c.f.Write(b) }
func (c *vsockConn) Close() error                       { return c.f.Close() }
func (c *vsockConn) LocalAddr() net.Addr                { return &net.UnixAddr{Name: "vsock", Net: "vsock"} }
func (c *vsockConn) RemoteAddr() net.Addr               { return &net.UnixAddr{Name: "vsock", Net: "vsock"} }
func (c *vsockConn) SetDeadline(t time.Time) error      { return c.f.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.f.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.f.SetWriteDeadline(t) }

func dialVsock(cid, port int) (net.Conn, error) {
	sa := &unix.SockaddrVM{CID: uint32(cid), Port: uint32(port)}
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return nil, fmt.Errorf("socket: %w", err)
		}
		err = unix.Connect(fd, sa)
		if err == nil {
			f := os.NewFile(uintptr(fd), "vsock")
			return &vsockConn{f: f}, nil
		}
		_ = unix.Close(fd)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
}

type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		nfd, _, err := unix.Accept(l.fd)
		if err == nil {
			f := os.NewFile(uintptr(nfd), "vsock-accept")
			return &vsockConn{f: f}, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return nil, err
	}
}

func (l *vsockListener) Close() error {
	return unix.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockListenerAddr{port: l.port}
}

type vsockListenerAddr struct {
	port uint32
}

func (a *vsockListenerAddr) Network() string { return "vsock" }
func (a *vsockListenerAddr) String() string  { return fmt.Sprintf("vsock:%d", a.port) }

func listenVsock(port uint32) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock bind: %w", err)
	}
	if err := unix.Listen(fd, 32); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &vsockListener{fd: fd, port: port}, nil
}
