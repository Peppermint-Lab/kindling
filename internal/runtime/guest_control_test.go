package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestExecGuestHTTP(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()

		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
		if req.URL.Path != "/exec" {
			t.Errorf("path = %s, want /exec", req.URL.Path)
		}
		var body guestExecRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if got := strings.Join(body.Argv, " "); got != "echo hello" {
			t.Errorf("argv = %q", got)
		}
		if body.Cwd != "/workspace" {
			t.Errorf("cwd = %q", body.Cwd)
		}
		if len(body.Env) != 1 || body.Env[0] != "FOO=bar" {
			t.Errorf("env = %#v", body.Env)
		}

		respBody, _ := json.Marshal(guestExecJSON{ExitCode: 0, Output: "hello\n"})
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: int64(len(respBody)),
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(respBody)),
		}
		resp.Header.Set("Content-Type", "application/json")
		_ = resp.Write(server)
	}()

	out, err := execGuestHTTP(context.Background(), client, []string{"echo", "hello"}, "/workspace", []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("execGuestHTTP: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", out.ExitCode)
	}
	if out.Output != "hello\n" {
		t.Fatalf("output = %q, want %q", out.Output, "hello\n")
	}
	<-done
}

func TestReadAndWriteGuestFileHTTP(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()

			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Errorf("read request: %v", err)
				return
			}
			if req.Method != http.MethodGet {
				t.Errorf("method = %s, want GET", req.Method)
			}
			if req.URL.Path != "/fs" {
				t.Errorf("path = %s, want /fs", req.URL.Path)
			}
			if got := req.URL.Query().Get("path"); got != "/workspace/demo.txt" {
				t.Errorf("query path = %q", got)
			}

			body := []byte("sandbox-data")
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ProtoMajor:    1,
				ProtoMinor:    1,
				ContentLength: int64(len(body)),
				Header:        make(http.Header),
				Body:          io.NopCloser(bytes.NewReader(body)),
			}
			resp.Header.Set("Content-Type", "application/octet-stream")
			_ = resp.Write(server)
		}()

		out, err := readGuestFileHTTP(context.Background(), client, "/workspace/demo.txt")
		if err != nil {
			t.Fatalf("readGuestFileHTTP: %v", err)
		}
		if string(out) != "sandbox-data" {
			t.Fatalf("read body = %q", string(out))
		}
		<-done
	})

	t.Run("write", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()

			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Errorf("read request: %v", err)
				return
			}
			if req.Method != http.MethodPut {
				t.Errorf("method = %s, want PUT", req.Method)
			}
			if got := req.URL.Query().Get("path"); got != "/workspace/demo.txt" {
				t.Errorf("query path = %q", got)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
				return
			}
			if string(body) != "updated-data" {
				t.Errorf("body = %q", string(body))
			}

			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ProtoMajor:    1,
				ProtoMinor:    1,
				ContentLength: 2,
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader("ok")),
			}
			resp.Header.Set("Content-Type", "text/plain")
			_ = resp.Write(server)
		}()

		if err := writeGuestFileHTTP(context.Background(), client, "/workspace/demo.txt", []byte("updated-data")); err != nil {
			t.Fatalf("writeGuestFileHTTP: %v", err)
		}
		<-done
	})
}

func TestStreamGuestHTTP(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()

		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
		if req.URL.Path != "/shell" {
			t.Errorf("path = %s, want /shell", req.URL.Path)
		}
		if got := req.Header.Get("Upgrade"); got != "kindling-shell-v1" {
			t.Errorf("upgrade = %q", got)
		}
		resp := &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Status:     "101 Switching Protocols",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
		}
		resp.Header.Set("Connection", "Upgrade")
		resp.Header.Set("Upgrade", "kindling-shell-v1")
		_ = resp.Write(server)
		_, _ = server.Write([]byte("shell-ready"))
	}()

	stream, err := streamGuestHTTP(context.Background(), client, []string{"/bin/sh"}, "/workspace", nil)
	if err != nil {
		t.Fatalf("streamGuestHTTP: %v", err)
	}
	defer stream.Close()
	buf := make([]byte, len("shell-ready"))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read upgraded stream: %v", err)
	}
	if string(buf) != "shell-ready" {
		t.Fatalf("stream payload = %q", string(buf))
	}
	<-done
}
