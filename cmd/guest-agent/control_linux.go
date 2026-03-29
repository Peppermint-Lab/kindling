//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/kindlingvm/kindling/internal/shellwire"
)

const guestControlVsockPort uint32 = 1028

type guestExecRequest struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

type guestExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

func startControlServer(ref *appRef) {
	_ = ref
	ln, err := listenVsock(guestControlVsockPort)
	if err != nil {
		log.Printf("control vsock listen: %v", err)
		return
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleControlConn(c)
		}
	}()
}

func handleControlConn(c net.Conn) {
	defer c.Close()
	req, err := http.ReadRequest(bufioReader(c))
	if err != nil {
		writeControlString(c, http.StatusBadRequest, "bad request")
		return
	}
	defer req.Body.Close()

	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/exec":
		handleExecRequest(c, req)
	case req.Method == http.MethodPost && req.URL.Path == "/shell":
		handleShellStream(c, req)
	case req.Method == http.MethodGet && req.URL.Path == "/fs":
		handleReadFile(c, req)
	case req.Method == http.MethodPut && req.URL.Path == "/fs":
		handleWriteFile(c, req)
	case req.Method == http.MethodPost && req.URL.Path == "/tcp-connect":
		handleTCPConnect(c, req)
	default:
		writeControlString(c, http.StatusNotFound, "not found")
	}
}

func handleExecRequest(c net.Conn, req *http.Request) {
	payload, err := decodeGuestExecRequest(req)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, "invalid json")
		return
	}
	cmd := guestExecCmd(payload)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	b, _ := json.Marshal(guestExecResponse{ExitCode: exitCode, Output: string(out)})
	writeControlBytes(c, http.StatusOK, "application/json", b)
}

func handleShellStream(c net.Conn, req *http.Request) {
	payload, err := decodeGuestExecRequest(req)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, "invalid json")
		return
	}
	cmd := guestExecCmd(payload)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		writeControlString(c, http.StatusInternalServerError, "pty start failed")
		return
	}
	defer ptmx.Close()

	writeSwitchingProtocols(c, "kindling-shell-v1")

	sendCh := make(chan shellwire.Frame, 64)
	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		enc := shellwire.NewEncoder(c)
		for frame := range sendCh {
			if err := enc.Encode(frame); err != nil {
				return
			}
		}
	}()
	sendFrame := func(frame shellwire.Frame) {
		select {
		case sendCh <- frame:
		default:
			sendCh <- frame
		}
	}
	sendFrame(shellwire.Frame{Type: "ready"})

	readDone := make(chan struct{}, 1)
	go func() {
		defer func() { readDone <- struct{}{} }()
		dec := shellwire.NewDecoder(c)
		for {
			frame, err := dec.Decode()
			if err != nil {
				_ = ptmx.Close()
				return
			}
			switch frame.Type {
			case "stdin":
				if frame.Data != "" {
					_, _ = io.WriteString(ptmx, frame.Data)
				}
			case "resize":
				if frame.Width > 0 && frame.Height > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{
						Cols: uint16(frame.Width),
						Rows: uint16(frame.Height),
					})
				}
			}
		}
	}()

	outputDone := make(chan struct{}, 1)
	go func() {
		defer func() { outputDone <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				sendFrame(shellwire.Frame{Type: "stdout", Data: string(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()

	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				sendFrame(shellwire.Frame{Type: "heartbeat"})
			}
		}
	}()

	waitErr := cmd.Wait()
	close(heartbeatStop)
	<-outputDone
	<-readDone
	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
			sendFrame(shellwire.Frame{Type: "error", Error: waitErr.Error()})
		}
	}
	sendFrame(shellwire.Frame{Type: "exit", ExitCode: &exitCode})
	close(sendCh)
	sendWG.Wait()
}

func handleTCPConnect(c net.Conn, req *http.Request) {
	port, err := requestedGuestTCPPort(req.URL)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, err.Error())
		return
	}
	upstream, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		writeControlString(c, http.StatusServiceUnavailable, "tcp connect failed")
		return
	}
	defer upstream.Close()

	writeSwitchingProtocols(c, "kindling-tcp-v1")

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, c)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, upstream)
		done <- struct{}{}
	}()
	<-done
	_ = c.Close()
	<-done
}

func handleReadFile(c net.Conn, req *http.Request) {
	filePath, err := requestedGuestPath(req.URL)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, err.Error())
		return
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		writeControlString(c, http.StatusNotFound, "file not found")
		return
	}
	writeControlBytes(c, http.StatusOK, "application/octet-stream", data)
}

func handleWriteFile(c net.Conn, req *http.Request) {
	filePath, err := requestedGuestPath(req.URL)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, err.Error())
		return
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		writeControlString(c, http.StatusBadRequest, "read body")
		return
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		writeControlString(c, http.StatusInternalServerError, "mkdir")
		return
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		writeControlString(c, http.StatusInternalServerError, "write failed")
		return
	}
	writeControlBytes(c, http.StatusOK, "text/plain", []byte("ok"))
}

func requestedGuestPath(u *url.URL) (string, error) {
	p := strings.TrimSpace(u.Query().Get("path"))
	if p == "" || !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.Clean(p), nil
}

func requestedGuestTCPPort(u *url.URL) (int, error) {
	raw := strings.TrimSpace(u.Query().Get("port"))
	if raw == "" {
		return 0, fmt.Errorf("port is required")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return port, nil
}

func decodeGuestExecRequest(req *http.Request) (guestExecRequest, error) {
	var payload guestExecRequest
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		return guestExecRequest{}, err
	}
	if len(payload.Argv) == 0 {
		return guestExecRequest{}, fmt.Errorf("missing argv")
	}
	return payload, nil
}

func guestExecCmd(payload guestExecRequest) *exec.Cmd {
	cmd := exec.Command(payload.Argv[0], payload.Argv[1:]...)
	cwd := strings.TrimSpace(payload.Cwd)
	if cwd == "" {
		if _, err := os.Stat("/app"); err == nil {
			cwd = "/app"
		} else {
			cwd = "/"
		}
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), payload.Env...)
	if !hasEnvKey(payload.Env, "TERM") {
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	}
	return cmd
}

func hasEnvKey(env []string, key string) bool {
	prefix := strings.TrimSpace(key) + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func writeControlString(c net.Conn, status int, msg string) {
	writeControlBytes(c, status, "text/plain; charset=utf-8", []byte(msg))
}

func writeControlBytes(c net.Conn, status int, contentType string, body []byte) {
	resp := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
	}
	resp.Header.Set("Content-Type", contentType)
	_ = resp.Write(c)
}

func writeSwitchingProtocols(c net.Conn, upgrade string) {
	resp := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Status:     fmt.Sprintf("%d %s", http.StatusSwitchingProtocols, http.StatusText(http.StatusSwitchingProtocols)),
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	resp.Header.Set("Connection", "Upgrade")
	resp.Header.Set("Upgrade", upgrade)
	_ = resp.Write(c)
}

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }
