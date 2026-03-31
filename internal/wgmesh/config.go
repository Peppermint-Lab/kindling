package wgmesh

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// IfaceName is the WireGuard interface Kindling manages.
const IfaceName = "wg0"

// Enabled reports whether this process should run WireGuard mesh automation.
func Enabled() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	v := strings.TrimSpace(strings.ToLower(os.Getenv("KINDLING_WG_MESH")))
	return v == "1" || v == "true" || v == "yes"
}

// RequireLinux returns an error when mesh is requested on a non-Linux OS.
func RequireLinux() error {
	if !Enabled() {
		return nil
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("KINDLING_WG_MESH is set but WireGuard mesh is only supported on linux (got GOOS=%s)", runtime.GOOS)
	}
	return nil
}

// Endpoint returns KINDLING_WG_ENDPOINT (host:port UDP underlay for this node).
func Endpoint() string {
	return strings.TrimSpace(os.Getenv("KINDLING_WG_ENDPOINT"))
}

// ListenPort returns KINDLING_WG_LISTEN_PORT or 51820.
func ListenPort() (int, error) {
	s := strings.TrimSpace(os.Getenv("KINDLING_WG_LISTEN_PORT"))
	if s == "" {
		return 51820, nil
	}
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid KINDLING_WG_LISTEN_PORT %q", s)
	}
	return p, nil
}
