//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"log"
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
	case req.Method == http.MethodGet && req.URL.Path == "/fs":
		handleReadFile(c, req)
	case req.Method == http.MethodPut && req.URL.Path == "/fs":
		handleWriteFile(c, req)
	default:
		writeControlString(c, http.StatusNotFound, "not found")
	}
}

func handleExecRequest(c net.Conn, req *http.Request) {
	var payload guestExecRequest
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		writeControlString(c, http.StatusBadRequest, "invalid json")
		return
	}
	if len(payload.Argv) == 0 {
		writeControlString(c, http.StatusBadRequest, "missing argv")
		return
	}
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

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }
