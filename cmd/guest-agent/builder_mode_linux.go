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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const builderExecVsockPort uint32 = 1027

type execRequest struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

// runBuilderMode runs the Kindling OS builder microVM: mounts workspace + builder rootfs,
// then serves POST /exec on vsock to run whitelisted buildah commands inside a chroot.
func runBuilderMode(cfg *ConfigResponse) error {
	if err := mountBuilderVolumes(); err != nil {
		return err
	}
	if err := configureNetwork(cfg); err != nil {
		log.Printf("warning: network config failed: %v", err)
	}
	setHostname(cfg.Hostname)

	ln, err := listenVsock(builderExecVsockPort)
	if err != nil {
		return fmt.Errorf("listen builder exec vsock: %w", err)
	}
	log.Printf("builder: listening for exec on vsock port %d", builderExecVsockPort)

	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := notifyReady(); err != nil {
			log.Printf("builder: notify host ready: %v", err)
		} else {
			log.Println("builder: notified host ready")
		}
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("builder vsock accept: %v", err)
			continue
		}
		go handleBuilderExecConn(c)
	}
}

func mountBuilderVolumes() error {
	os.Setenv("PATH", "/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin")

	for _, d := range []string{"/mnt", "/mnt/workspace", "/mnt/builderroot", "/app"} {
		_ = os.MkdirAll(d, 0o755)
	}

	// Host attaches an optional empty "app" virtiofs (same as workload VMs).
	if err := syscall.Mount("app", "/app", "virtiofs", 0, ""); err != nil {
		log.Printf("builder: virtiofs app mount: %v", err)
	}

	if err := syscall.Mount("workspace", "/mnt/workspace", "virtiofs", 0, ""); err != nil {
		return fmt.Errorf("mount workspace virtiofs: %w", err)
	}
	if err := syscall.Mount("builder", "/mnt/builderroot", "virtiofs", 0, ""); err != nil {
		return fmt.Errorf("mount builder virtiofs: %w", err)
	}

	_ = os.RemoveAll("/mnt/builderroot/workspace")
	if err := os.MkdirAll("/mnt/builderroot/workspace", 0o755); err != nil {
		return fmt.Errorf("mkdir workspace under chroot: %w", err)
	}
	if err := unix.Mount("/mnt/workspace", "/mnt/builderroot/workspace", "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind workspace into chroot: %w", err)
	}

	log.Println("builder: mounted workspace and builder rootfs")
	return nil
}

func handleBuilderExecConn(conn net.Conn) {
	defer conn.Close()

	reqData, err := readFullHTTPRequest(conn)
	if err != nil {
		writeHTTPString(conn, 400, "bad request")
		return
	}
	if !bytes.Contains(reqData, []byte("POST /exec")) {
		writeHTTPString(conn, 404, "not found")
		return
	}
	body := extractHTTPBodyBytes(reqData)
	var req execRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPString(conn, 400, "invalid json")
		return
	}
	if len(req.Argv) < 1 {
		writeHTTPString(conn, 400, "missing argv")
		return
	}
	if filepath.Base(req.Argv[0]) != "buildah" {
		writeHTTPString(conn, 403, "only buildah is allowed")
		return
	}

	cwd := req.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}

	env := append(os.Environ(), req.Env...)
	// Run argv inside chroot with working directory set via shell so build context resolves.
	inner := append([]string(nil), req.Argv...)
	argsJoined := shellQuoteArgs(inner)
	shScript := fmt.Sprintf("cd %s && exec %s", shellQuoteSingle(cwd), argsJoined)
	cmd := exec.Command("chroot", "/mnt/builderroot", "sh", "-c", shScript)
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}

	msg := string(out)
	if msg != "" && !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	msg += fmt.Sprintf("KINDLING_EXIT_CODE %d\n", code)
	writeHTTPBytes(conn, 200, []byte(msg))
}

func shellQuoteSingle(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'"'"'`) + `'`
}

func shellQuoteArgs(argv []string) string {
	b := new(strings.Builder)
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuoteSingle(a))
	}
	return b.String()
}

func readFullHTTPRequest(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 8192)
	for {
		hdrEnd := bytes.Index(buf.Bytes(), []byte("\r\n\r\n"))
		if hdrEnd >= 0 {
			header := buf.Bytes()[:hdrEnd]
			cl := parseContentLength(header)
			need := hdrEnd + 4
			if cl > 0 {
				need += cl
			}
			if buf.Len() >= need {
				return buf.Bytes()[:need], nil
			}
		}

		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if err == io.EOF {
				return buf.Bytes(), nil
			}
			return nil, err
		}
		if buf.Len() > 16<<20 {
			return nil, fmt.Errorf("request too large")
		}
	}
}

func parseContentLength(header []byte) int {
	sc := bufio.NewScanner(bytes.NewReader(header))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, ':'); i > 0 {
			name := strings.ToLower(strings.TrimSpace(line[:i]))
			if name == "content-length" {
				v := strings.TrimSpace(line[i+1:])
				n, err := strconv.Atoi(v)
				if err != nil {
					return 0
				}
				return n
			}
		}
	}
	return 0
}


func extractHTTPBodyBytes(req []byte) []byte {
	idx := bytes.Index(req, []byte("\r\n\r\n"))
	if idx < 0 {
		return nil
	}
	return req[idx+4:]
}

func writeHTTPString(conn net.Conn, status int, msg string) {
	writeHTTPBytes(conn, status, []byte(msg))
}

func writeHTTPBytes(conn net.Conn, status int, body []byte) {
	reason := "OK"
	if status != 200 {
		reason = "Error"
	}
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", status, reason, len(body))
	_, _ = conn.Write([]byte(resp))
	_, _ = conn.Write(body)
}
