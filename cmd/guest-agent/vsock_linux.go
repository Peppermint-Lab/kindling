package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"
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
