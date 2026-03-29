//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kindlingvm/kindling/internal/shellwire"
	"golang.org/x/term"
)

// localAPIClient is a client for the kindling-mac daemon's Unix socket HTTP API.
type localAPIClient struct {
	socketPath string
	transport  *http.Transport
}

type localUpgradedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *localUpgradedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func newLocalAPI(socketPath string) *localAPIClient {
	return &localAPIClient{
		socketPath: socketPath,
		transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func (a *localAPIClient) do(ctx context.Context, method, path string, body, out any) error {
	if err := a.ensureDaemon(ctx); err != nil {
		return err
	}

	var reqBody io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reqBody = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: a.transport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to kindling-mac daemon (is it running?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		if errMsg, ok := errResp["error"]; ok {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("API error %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *localAPIClient) doContext(ctx context.Context, method, path string, body, out any) error {
	return a.do(ctx, method, path, body, out)
}

func (a *localAPIClient) Status(ctx context.Context) error {
	// Get box status.
	var box map[string]any
	if err := a.do(ctx, http.MethodGet, "/box.status", nil, &box); err != nil {
		fmt.Printf("box: not configured\n")
	} else {
		fmt.Printf("box: %s (id=%v)\n", box["status"], box["id"])
	}

	// Get temp list.
	var temps []map[string]any
	if err := a.do(ctx, http.MethodGet, "/temp.list", nil, &temps); err != nil {
		fmt.Printf("temp list: %v\n", err)
	} else {
		fmt.Printf("temp VMs: %d running\n", len(temps))
		for _, t := range temps {
			fmt.Printf("  %s  %s  %s\n", t["id"], t["name"], t["status"])
		}
	}

	// Get all VMs.
	var vms []map[string]any
	if err := a.do(ctx, http.MethodGet, "/vm.list", nil, &vms); err != nil {
		fmt.Printf("vm list: %v\n", err)
	} else {
		fmt.Printf("total VMs: %d\n", len(vms))
	}

	return nil
}

func (a *localAPIClient) ListVMs(ctx context.Context) error {
	var vms []map[string]any
	if err := a.do(ctx, http.MethodGet, "/vm.list", nil, &vms); err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("no VMs")
		return nil
	}
	fmt.Printf("%-36s  %-12s  %-8s  %s\n", "ID", "HOST GROUP", "STATUS", "NAME")
	for _, vm := range vms {
		fmt.Printf("%-36s  %-12s  %-8s  %s\n", vm["id"], vm["host_group"], vm["status"], vm["name"])
	}
	return nil
}

func (a *localAPIClient) RunShell(ctx context.Context, id string, argv []string, cwd string, env []string) error {
	stream, err := a.openShell(ctx, id, argv, cwd, env)
	if err != nil {
		return err
	}
	defer stream.Close()

	var oldState *term.State
	stdinFD := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFD) {
		state, err := term.MakeRaw(stdinFD)
		if err == nil {
			oldState = state
			defer term.Restore(stdinFD, state)
		}
	}

	enc := shellwire.NewEncoder(stream)
	dec := shellwire.NewDecoder(stream)
	var writeMu sync.Mutex
	sendFrame := func(frame shellwire.Frame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(frame)
	}
	if term.IsTerminal(stdinFD) {
		if width, height, err := term.GetSize(stdinFD); err == nil {
			_ = sendFrame(shellwire.Frame{Type: "resize", Width: width, Height: height})
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if !term.IsTerminal(stdinFD) {
				continue
			}
			width, height, err := term.GetSize(stdinFD)
			if err != nil {
				continue
			}
			_ = sendFrame(shellwire.Frame{Type: "resize", Width: width, Height: height})
		}
	}()

	copyErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := sendFrame(shellwire.Frame{Type: "stdin", Data: string(buf[:n])}); werr != nil {
					copyErr <- werr
					return
				}
			}
			if err != nil {
				copyErr <- err
				return
			}
		}
	}()

	var exitCode int
	var sawExit bool
	var outErr error
	for {
		frame, err := dec.Decode()
		if err != nil {
			outErr = err
			break
		}
		switch frame.Type {
		case "stdout", "stderr":
			if frame.Data != "" {
				if _, err := io.WriteString(os.Stdout, frame.Data); err != nil {
					outErr = err
				}
			}
		case "error":
			if frame.Error != "" {
				_, _ = fmt.Fprintln(os.Stderr, frame.Error)
			}
		case "exit":
			sawExit = true
			if frame.ExitCode != nil {
				exitCode = *frame.ExitCode
			}
		}
		if outErr != nil || sawExit {
			break
		}
	}
	if oldState != nil {
		fmt.Fprintln(os.Stdout)
	}
	_ = stream.Close()
	if outErr != nil && !errors.Is(outErr, net.ErrClosed) && !errors.Is(outErr, io.EOF) {
		return outErr
	}
	if sawExit && exitCode != 0 {
		return fmt.Errorf("shell exited with code %d", exitCode)
	}
	select {
	case inErr := <-copyErr:
		if inErr != nil && !errors.Is(inErr, net.ErrClosed) && !errors.Is(inErr, io.EOF) {
			return inErr
		}
	default:
	}
	return nil
}

func (a *localAPIClient) openShell(ctx context.Context, id string, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	if err := a.ensureDaemon(ctx); err != nil {
		return nil, err
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", a.socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to kindling-mac daemon (is it running?): %w", err)
	}

	reqBody, err := json.Marshal(map[string]any{
		"id":   id,
		"argv": argv,
		"cwd":  cwd,
		"env":  env,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/vm.shell", bytes.NewReader(reqBody))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-shell-v1")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		conn.Close()
		return nil, fmt.Errorf("open shell: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return &localUpgradedConn{Conn: conn, reader: reader}, nil
}

func (a *localAPIClient) ensureDaemon(ctx context.Context) error {
	if a.socketReachable() {
		return nil
	}

	kindlingMacBin, err := kindlingMacBinary()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, kindlingMacBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("start kindling-mac: %s", msg)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := a.waitForSocket(waitCtx); err != nil {
		return fmt.Errorf("start kindling-mac: %w", err)
	}
	return nil
}

func kindlingMacBinary() (string, error) {
	if path := os.Getenv("KINDLING_MAC_BIN"); path != "" {
		return path, nil
	}

	if exePath, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exePath), "kindling-mac")
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
			return sibling, nil
		}
	}

	path, err := exec.LookPath("kindling-mac")
	if err != nil {
		return "", fmt.Errorf("kindling-mac is not installed or not on PATH")
	}
	return path, nil
}

func (a *localAPIClient) socketReachable() bool {
	conn, err := net.DialTimeout("unix", a.socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (a *localAPIClient) waitForSocket(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if a.socketReachable() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for kindling-mac daemon")
		case <-ticker.C:
		}
	}
}
