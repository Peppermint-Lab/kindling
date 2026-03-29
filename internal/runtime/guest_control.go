package runtime

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

const guestControlDefaultTimeout = 30 * time.Second

type guestExecRequest struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

type guestExecJSON struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

type upgradedGuestConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *upgradedGuestConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func execGuestHTTP(ctx context.Context, c net.Conn, argv []string, cwd string, env []string) (GuestExecResult, error) {
	body, err := json.Marshal(guestExecRequest{Argv: argv, Cwd: cwd, Env: env})
	if err != nil {
		return GuestExecResult{}, err
	}
	resp, err := doGuestHTTPRequest(ctx, c, http.MethodPost, "/exec", bytes.NewReader(body), "application/json")
	if err != nil {
		return GuestExecResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return GuestExecResult{}, fmt.Errorf("guest exec: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out guestExecJSON
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return GuestExecResult{}, err
	}
	return GuestExecResult{ExitCode: out.ExitCode, Output: out.Output}, nil
}

func readGuestFileHTTP(ctx context.Context, c net.Conn, filePath string) ([]byte, error) {
	p := "/fs?path=" + url.QueryEscape(filePath)
	resp, err := doGuestHTTPRequest(ctx, c, http.MethodGet, p, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("guest read file: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(resp.Body)
}

func writeGuestFileHTTP(ctx context.Context, c net.Conn, filePath string, data []byte) error {
	p := "/fs?path=" + url.QueryEscape(filePath)
	resp, err := doGuestHTTPRequest(ctx, c, http.MethodPut, p, bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("guest write file: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func streamGuestHTTP(ctx context.Context, c net.Conn, argv []string, cwd string, env []string) (io.ReadWriteCloser, error) {
	body, err := json.Marshal(guestExecRequest{Argv: argv, Cwd: cwd, Env: env})
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	} else {
		_ = c.SetDeadline(time.Now().Add(guestControlDefaultTimeout))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/shell", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-shell-v1")
	if err := req.Write(c); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("guest stream: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_ = c.SetDeadline(time.Time{})
	return &upgradedGuestConn{Conn: c, reader: reader}, nil
}

func tcpGuestHTTP(ctx context.Context, c net.Conn, port int) (io.ReadWriteCloser, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	} else {
		_ = c.SetDeadline(time.Now().Add(guestControlDefaultTimeout))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/tcp-connect?port="+url.QueryEscape(fmt.Sprintf("%d", port)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-tcp-v1")
	if err := req.Write(c); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("guest tcp stream: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_ = c.SetDeadline(time.Time{})
	return &upgradedGuestConn{Conn: c, reader: reader}, nil
}

func doGuestHTTPRequest(ctx context.Context, c net.Conn, method, reqPath string, body io.Reader, contentType string) (*http.Response, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
	} else {
		_ = c.SetDeadline(time.Now().Add(guestControlDefaultTimeout))
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
	if err := req.Write(c); err != nil {
		return nil, err
	}
	return http.ReadResponse(bufio.NewReader(c), req)
}
