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
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleVMTCP(t *testing.T) {
	mgr := &Manager{
		vms: map[string]*localVM{
			"box-1": {
				controlDialer: func() (net.Conn, error) {
					server, client := net.Pipe()
					go func() {
						defer server.Close()
						req, err := http.ReadRequest(bufio.NewReader(server))
						if err != nil {
							return
						}
						if req.URL.Path != "/tcp-connect" || req.URL.Query().Get("port") != "5432" {
							http.Error(responseWriterFromConn(server), "unexpected request", http.StatusBadRequest)
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
						if err := resp.Write(server); err != nil {
							return
						}
						buf := make([]byte, 4)
						if _, err := io.ReadFull(server, buf); err != nil {
							return
						}
						_, _ = server.Write(buf)
					}()
					return client, nil
				},
			},
		},
	}
	server := &Server{mgr: mgr}
	handler := withVMManager(mgr, server.handleVMTCP)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	reqBody, err := json.Marshal(map[string]any{
		"id":   "box-1",
		"port": 5432,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer conn.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "kindling-tcp-v1")
	if err := req.Write(conn); err != nil {
		t.Fatalf("write request: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %q, want 101", resp.StatusCode, string(body))
	}

	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read tunneled payload: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("payload echo = %q, want ping", string(buf))
	}
}

func TestHandleVMTCPRejectsInvalidPort(t *testing.T) {
	mgr := &Manager{vms: map[string]*localVM{}}
	server := &Server{mgr: mgr}
	handler := withVMManager(mgr, server.handleVMTCP)

	req := httptest.NewRequest(http.MethodPost, "/vm.tcp", strings.NewReader(`{"id":"box-1","port":70000}`))
	rec := httptest.NewRecorder()
	handler(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

type connResponseWriter struct {
	conn net.Conn
}

func responseWriterFromConn(conn net.Conn) http.ResponseWriter {
	return &connResponseWriter{conn: conn}
}

func (w *connResponseWriter) Header() http.Header {
	return make(http.Header)
}

func (w *connResponseWriter) Write(b []byte) (int, error) {
	return w.conn.Write(b)
}

func (w *connResponseWriter) WriteHeader(statusCode int) {
	resp := &http.Response{
		StatusCode:    statusCode,
		Status:        fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		ProtoMajor:    1,
		ProtoMinor:    1,
		ContentLength: 0,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader("")),
	}
	_ = resp.Write(w.conn)
}

func TestManagerOpenTCPRequiresValidPort(t *testing.T) {
	mgr := &Manager{vms: map[string]*localVM{"box-1": {}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := mgr.OpenTCP(ctx, "box-1", 0); err == nil {
		t.Fatal("expected invalid port error")
	}
}
