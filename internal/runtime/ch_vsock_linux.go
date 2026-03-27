//go:build linux

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kindlingvm/kindling/internal/chbridge"
)

type guestConfig struct {
	Env             []string `json:"env"`
	IPAddr          string   `json:"ip_addr"`
	IPGW            string   `json:"ip_gw"`
	Hostname        string   `json:"hostname"`
	Port            int      `json:"port"`
	VolumeMountPath string   `json:"volume_mount_path,omitempty"`
}

func (r *CloudHypervisorRuntime) startGuestVsockServer(ctx context.Context, inst Instance, ai *cloudHypervisorInstance, guestCIDR, hostIP string, port int) error {
	socketPath := ai.socketBase + "_" + strconv.Itoa(cloudHypervisorVsockPort)
	_ = os.Remove(socketPath)
	_ = os.Remove(ai.socketBase)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen guest vsock uds: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		cfg := guestConfig{
			Env:      envWithPort(inst.Env, port),
			IPAddr:   guestCIDR,
			IPGW:     hostIP,
			Hostname: fmt.Sprintf("kindling-%s", inst.ID.String()[:8]),
			Port:     port,
		}
		if inst.PersistentVolume != nil {
			cfg.VolumeMountPath = inst.PersistentVolume.MountPath
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cfg)
	})
	mux.HandleFunc("POST /logs", func(w http.ResponseWriter, req *http.Request) {
		scanner := bufio.NewScanner(req.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			ai.logMu.Lock()
			ai.logs = append(ai.logs, scanner.Text())
			if len(ai.logs) > 1000 {
				ai.logs = ai.logs[len(ai.logs)-1000:]
			}
			ai.logMu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /ready", func(w http.ResponseWriter, _ *http.Request) {
		ai.markReady()
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = lis.Close()
		r.cleanupGuestVsock(ai.socketBase)
	}()
	go func() {
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			slog.Debug("cloud-hypervisor vsock server ended", "error", err)
		}
	}()
	return nil
}

func (r *CloudHypervisorRuntime) cleanupGuestVsock(base string) {
	_ = os.Remove(base)
	_ = os.Remove(base + "_" + strconv.Itoa(cloudHypervisorVsockPort))
}

func dialCloudHypervisorGuestOverUDS(vsockUDS string, port uint32) (net.Conn, error) {
	return chbridge.DialGuestOverUDS(vsockUDS, port)
}

func ioCopyClose(dst, src net.Conn) (int64, error) {
	n, err := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return n, err
}

func envWithPort(env []string, port int) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "PORT=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, fmt.Sprintf("PORT=%d", port))
}

func waitForGuestReady(ctx context.Context, ready <-chan struct{}, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("timed out after %s", timeout)
	}
}

func (ai *cloudHypervisorInstance) markReady() {
	ai.once.Do(func() {
		close(ai.ready)
	})
}
