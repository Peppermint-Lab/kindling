//go:build linux

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cloudHypervisorPingResponse struct {
	Version string `json:"version"`
}

func cloudHypervisorAPIClient(apiSocket string) *http.Client {
	return &http.Client{
		Timeout: chAPIClientTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", apiSocket)
			},
		},
	}
}

func (r *CloudHypervisorRuntime) pingVMM(ctx context.Context, apiSocket string) (cloudHypervisorPingResponse, error) {
	var out cloudHypervisorPingResponse
	client := cloudHypervisorAPIClient(apiSocket)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vmm.ping", nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("cloud-hypervisor ping: %s", strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

func (r *CloudHypervisorRuntime) putVMM(ctx context.Context, apiSocket, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal VMM payload: %w", err)
	}
	client := cloudHypervisorAPIClient(apiSocket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/api/v1"+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create VMM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send VMM request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cloud-hypervisor api %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func waitForCloudHypervisorAPI(ctx context.Context, apiSocket string) error {
	deadline := time.Now().Add(chAPIReadyTimeout)
	for time.Now().Before(deadline) {
		client := cloudHypervisorAPIClient(apiSocket)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/vmm.ping", nil)
		if err == nil {
			resp, callErr := client.Do(req)
			if callErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(chAPIReadyPollInterval):
		}
	}
	return fmt.Errorf("cloud-hypervisor api socket %s did not become ready", apiSocket)
}

func waitForTCPPort(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, chTCPDialTimeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(chAPIReadyPollInterval):
		}
	}
	return fmt.Errorf("timed out waiting for %s", addr)
}

func startCloudHypervisorBridgeHelper(hostPort int, vsockUDS string) (*exec.Cmd, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin,
		"ch-bridge-proxy",
		"--listen", net.JoinHostPort("0.0.0.0", strconv.Itoa(hostPort)),
		"--vsock", vsockUDS,
		"--guest-port", strconv.Itoa(cloudHypervisorGuestBridgeVsockPort),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func writePIDFile(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600)
}

func terminatePIDFromFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read PID file %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("parse PID from %s: %w", path, err)
	}
	return terminatePID(pid)
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
