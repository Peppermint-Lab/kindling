//go:build !linux

package main

import (
	"fmt"
	"net"
)

func dialVsock(cid, port int) (net.Conn, error) {
	return nil, fmt.Errorf("vsock requires Linux")
}
