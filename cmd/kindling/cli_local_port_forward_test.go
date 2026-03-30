//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunLocalPortForwardExplicitHostPort(t *testing.T) {
	hostPort := freeTCPPort(t)
	port, ensureCalls, errOut, runErr := startAndExercisePortForward(t, hostPort)
	if runErr != nil {
		t.Fatalf("runLocalPortForward returned error: %v", runErr)
	}
	if port != hostPort {
		t.Fatalf("forwarded host port = %d, want %d", port, hostPort)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensure box calls = %d, want 1", ensureCalls)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
}

func TestRunLocalPortForwardEphemeralHostPort(t *testing.T) {
	port, ensureCalls, errOut, runErr := startAndExercisePortForward(t, 0)
	if runErr != nil {
		t.Fatalf("runLocalPortForward returned error: %v", runErr)
	}
	if port <= 0 {
		t.Fatalf("forwarded host port = %d, want ephemeral port", port)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensure box calls = %d, want 1", ensureCalls)
	}
	if errOut != "" {
		t.Fatalf("unexpected stderr: %s", errOut)
	}
}

func TestRunLocalPortForwardRejectsInvalidGuestPort(t *testing.T) {
	err := runLocalPortForward(context.Background(), localPortForwardOptions{
		guestPort: 70000,
		hostPort:  5432,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid guest port") {
		t.Fatalf("runLocalPortForward error = %v, want invalid guest port", err)
	}
}

func TestLocalAPIClientOpenTCP(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "kindling.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/vm.tcp" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var req struct {
				ID   string `json:"id"`
				Port int    `json:"port"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.ID != "box-1" || req.Port != 5432 {
				http.Error(w, "unexpected request", http.StatusBadRequest)
				return
			}
			h, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			conn, rw, err := h.Hijack()
			if err != nil {
				return
			}
			resp := &http.Response{
				StatusCode: http.StatusSwitchingProtocols,
				Status:     fmt.Sprintf("%d %s", http.StatusSwitchingProtocols, http.StatusText(http.StatusSwitchingProtocols)),
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     make(http.Header),
			}
			resp.Header.Set("Connection", "Upgrade")
			resp.Header.Set("Upgrade", "kindling-tcp-v1")
			if err := resp.Write(rw); err != nil {
				conn.Close()
				return
			}
			if err := rw.Flush(); err != nil {
				conn.Close()
				return
			}
			go echoConn(conn)
		}),
	}
	go func() {
		serverErr <- srv.Serve(ln)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := newLocalAPI(socketPath)
	stream, err := client.openTCP(context.Background(), "box-1", 5432)
	if err != nil {
		t.Fatalf("openTCP: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "hello"); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("stream echo = %q, want hello", string(buf))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown server: %v", err)
	}
	if err := <-serverErr; err != nil && err != http.ErrServerClosed {
		t.Fatalf("server error: %v", err)
	}
}

func startAndExercisePortForward(t *testing.T, hostPort int) (int, int32, string, error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	var errOut bytes.Buffer
	var ensureCalls int32

	done := make(chan error, 1)
	go func() {
		done <- runLocalPortForward(ctx, localPortForwardOptions{
			out:       &out,
			errOut:    &errOut,
			host:      "127.0.0.1",
			hostPort:  hostPort,
			guestPort: 5432,
			listen:    net.Listen,
			ensureBox: func(context.Context) (string, error) {
				atomic.AddInt32(&ensureCalls, 1)
				return "box-1", nil
			},
			openTCP: func(context.Context, string, int) (io.ReadWriteCloser, error) {
				server, client := net.Pipe()
				go echoConn(server)
				return client, nil
			},
		})
	}()

	forwardedPort := waitForForwardedPort(t, &out)
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(forwardedPort)))
	if err != nil {
		cancel()
		return 0, atomic.LoadInt32(&ensureCalls), errOut.String(), <-done
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, "ping"); err != nil {
		cancel()
		return forwardedPort, atomic.LoadInt32(&ensureCalls), errOut.String(), err
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		cancel()
		return forwardedPort, atomic.LoadInt32(&ensureCalls), errOut.String(), err
	}
	if string(buf) != "ping" {
		cancel()
		return forwardedPort, atomic.LoadInt32(&ensureCalls), errOut.String(), fmt.Errorf("unexpected echo %q", string(buf))
	}

	cancel()
	return forwardedPort, atomic.LoadInt32(&ensureCalls), errOut.String(), <-done
}

func waitForForwardedPort(t *testing.T, out *bytes.Buffer) int {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		text := out.String()
		if text != "" {
			lines := strings.Split(strings.TrimSpace(text), "\n")
			if len(lines) > 0 {
				fields := strings.Fields(lines[0])
				if len(fields) >= 2 {
					hostPort := strings.TrimPrefix(fields[1], "127.0.0.1:")
					if p, err := strconv.Atoi(strings.TrimSpace(hostPort)); err == nil && p > 0 {
						return p
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for forwarded port in output: %q", out.String())
	return 0
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func echoConn(conn io.ReadWriteCloser) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
