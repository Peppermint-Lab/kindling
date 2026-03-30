//go:build darwin

package macd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const guestControlTimeout = 30 * time.Second

type guestExecRequest struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

type guestExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

type upgradedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *upgradedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func execGuestHTTP(ctx context.Context, conn net.Conn, argv []string, cwd string, env []string) (int, string, error) {
	body, err := json.Marshal(guestExecRequest{Argv: argv, Cwd: cwd, Env: env})
	if err != nil {
		return 0, "", err
	}
	resp, err := doGuestHTTPRequest(ctx, conn, http.MethodPost, "/exec", bytes.NewReader(body), "application/json")
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return 0, "", fmt.Errorf("guest exec: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out guestExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, "", err
	}
	return out.ExitCode, out.Output, nil
}

func streamGuestHTTP(ctx context.Context, conn net.Conn, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	body, err := json.Marshal(guestExecRequest{Argv: argv, Cwd: cwd, Env: env})
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(guestControlTimeout))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/shell", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-shell-v1")
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("guest shell: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &upgradedConn{Conn: conn, reader: reader}, nil
}

func streamGuestTCPHTTP(ctx context.Context, conn net.Conn, port int) (io.ReadWriteCloser, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(guestControlTimeout))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/tcp-connect?port="+url.QueryEscape(fmt.Sprintf("%d", port)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-tcp-v1")
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("guest tcp stream: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &upgradedConn{Conn: conn, reader: reader}, nil
}

func doGuestHTTPRequest(ctx context.Context, conn net.Conn, method, reqPath string, body io.Reader, contentType string) (*http.Response, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(guestControlTimeout))
	}
	u := &url.URL{Scheme: "http", Host: "localhost", Path: path.Clean(reqPath)}
	if strings.Contains(reqPath, "?") {
		rawPath, rawQuery, _ := strings.Cut(reqPath, "?")
		u.Path = path.Clean(rawPath)
		u.RawQuery = rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(conn), req)
}
