package main

import (
	"fmt"
	"net"
	"syscall"
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
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("fileconn: %w", err)
	}

	return conn, nil
}
