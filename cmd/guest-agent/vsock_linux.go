package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sockaddrVM is the AF_VSOCK sockaddr structure.
type sockaddrVM struct {
	family    uint16
	reserved1 uint16
	port      uint32
	cid       uint32
	flags     uint8
	zero      [3]uint8
}

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
	fd, err := syscall.Socket(40, syscall.SOCK_STREAM, 0) // AF_VSOCK = 40
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	sa := sockaddrVM{
		family: 40,
		port:   uint32(port),
		cid:    uint32(cid),
	}

	_, _, errno := syscall.RawSyscall(
		syscall.SYS_CONNECT,
		uintptr(fd),
		uintptr(unsafe.Pointer(&sa)),
		unsafe.Sizeof(sa),
	)
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("connect: %w", errno)
	}

	f := os.NewFile(uintptr(fd), "vsock")
	return &vsockConn{f: f}, nil
}

type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	nfd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(nfd), "vsock-accept")
	return &vsockConn{f: f}, nil
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
