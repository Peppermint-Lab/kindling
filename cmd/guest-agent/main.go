// guest-agent is the init process (PID 1) inside each Kindling microVM.
//
// On boot it:
//  1. Connects to the host via vsock to fetch config (env vars, IP, hostname)
//  2. Configures networking (IP address, default route, DNS)
//  3. Starts the user's application as a child process
//  4. Streams application stdout/stderr back to the host via vsock
//  5. Handles SIGCHLD and shuts down cleanly
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	// vsockCID 2 = host from guest perspective.
	vsockCID = 2

	// configPort is the vsock port the host serves config on.
	configPort = 1024

	// tcpBridgeVsockPort is where this process listens for host-initiated vsock connections to relay to the app on loopback.
	// Must match internal/runtime tcpBridgeVsockPort (Apple VZ localhost forward).
	tcpBridgeVsockPort uint32 = 1025
)

// ConfigResponse is the JSON payload from the host's config endpoint.
type ConfigResponse struct {
	// Mode is "" or "app" for normal workloads; "builder" for the macOS OCI build microVM;
	// "ci" for a generic command-exec Linux VM used by workflow jobs.
	Mode            string   `json:"mode"`
	Env             []string `json:"env"`
	IPAddr          string   `json:"ip_addr"`
	IPGW            string   `json:"ip_gw"`
	DNSServers      []string `json:"dns_servers"`
	Hostname        string   `json:"hostname"`
	Port            int      `json:"port"`
	VolumeMountPath string   `json:"volume_mount_path"`
}

type commandSpec struct {
	name string
	args []string
}

func main() {
	log.SetPrefix("[guest-agent] ")
	log.SetFlags(log.Ltime)

	log.Println("starting guest agent (PID 1)")

	mountGuestBootstrap()

	cfg, err := fetchConfig()
	if err != nil {
		log.Fatalf("failed to fetch config: %v", err)
	}
	log.Printf("config received: mode=%s hostname=%s ip=%s env_count=%d", cfg.Mode, cfg.Hostname, cfg.IPAddr, len(cfg.Env))

	if cfg.Mode == "builder" || cfg.Mode == "ci" {
		log.Printf("%s mode: starting build/exec executor", cfg.Mode)
		if err := runBuilderMode(cfg); err != nil {
			log.Fatalf("%s mode: %v", cfg.Mode, err)
		}
		return
	}

	mountWorkloadVirtioApp()

	// Configure networking.
	if err := configureNetwork(cfg); err != nil {
		log.Printf("warning: network config failed: %v", err)
	}

	// Set hostname.
	setHostname(cfg.Hostname)

	// Chroot into the container rootfs if available.
	if isLocalShellMode(cfg.Mode) && !localRootFSAvailable() {
		log.Printf("skipping chroot for %s mode", cfg.Mode)
	} else {
		chrootIntoApp(cfg)
	}

	// Start log streaming to host.
	logWriter := startLogStream()

	appRef := &appRef{}
	startStatsServer(appRef)
	startControlServer(appRef)

	readyPort := cfg.Port
	if readyPort == 0 {
		readyPort = 3000
	}

	// Find and start the user's app.
	appCmd := startApp(cfg.Env, logWriter)
	appRef.set(appCmd)
	if appCmd == nil {
		if shouldKeepGuestReadyWithoutApp(cfg) {
			log.Printf("no application found, enabling shell-only guest")
			if shouldStartHostBridgeWithoutApp(cfg) {
				if err := startHostTCPBridge(readyPort); err != nil {
					log.Fatalf("host tcp vsock bridge: %v", err)
				}
			}
			if err := notifyReady(); err != nil {
				log.Printf("warning: ready notification failed: %v", err)
			} else {
				log.Printf("notified host ready (shell-only mode)")
			}
			select {} // keep control/stats servers alive for interactive use
		}
		log.Println("no application found, idling")
		select {} // block forever
	}

	// Cold starts can exceed 30s; the host still runs HTTP health checks after /ready.
	// Always bring up the vsock bridge and notify — do not skip notify when the TCP
	// probe is slow (previous if/else chain never called notifyReady on timeout).
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer readyCancel()
	if err := waitForTCPReady(readyCtx, fmt.Sprintf("127.0.0.1:%d", readyPort)); err != nil {
		log.Printf("warning: readiness probe timed out: %v", err)
	}
	if err := startHostTCPBridge(readyPort); err != nil {
		log.Fatalf("host tcp vsock bridge: %v", err)
	}
	if err := notifyReady(); err != nil {
		log.Printf("warning: ready notification failed: %v", err)
	} else {
		log.Printf("notified host ready (port %d)", readyPort)
	}

	// Wait for app to exit or signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	done := make(chan error, 1)
	go func() {
		done <- appCmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Printf("app exited with error: %v", err)
			os.Exit(1)
		}
		log.Println("app exited cleanly")
	case sig := <-sigCh:
		log.Printf("received signal %v, forwarding to app", sig)
		if appCmd.Process != nil {
			appCmd.Process.Signal(sig)
		}
		<-done
	}
}

func fetchConfig() (*ConfigResponse, error) {
	conn, err := dialVsock(vsockCID, configPort)
	if err != nil {
		return nil, fmt.Errorf("vsock connect: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return conn, nil
			},
		},
	}

	resp, err := client.Get("http://localhost/config")
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("HTTP GET /config: %w", err)
	}
	defer resp.Body.Close()

	var cfg ConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	return &cfg, nil
}

func configureNetwork(cfg *ConfigResponse) error {
	for _, cmd := range networkCommands(cfg) {
		if err := run(cmd.name, cmd.args...); err != nil {
			switch {
			case len(cmd.args) >= 4 && cmd.args[0] == "link" && cmd.args[1] == "set" && cmd.args[2] == "lo":
				return fmt.Errorf("bring up loopback: %w", err)
			case len(cmd.args) >= 4 && cmd.args[0] == "link" && cmd.args[1] == "set" && cmd.args[2] == "eth0":
				return fmt.Errorf("link up: %w", err)
			case len(cmd.args) >= 4 && cmd.args[0] == "addr" && cmd.args[1] == "add":
				return fmt.Errorf("add addr: %w", err)
			case len(cmd.args) >= 5 && cmd.args[0] == "route" && cmd.args[1] == "add":
				return fmt.Errorf("default route: %w", err)
			default:
				return err
			}
		}
	}

	os.MkdirAll("/etc", 0o755)
	if err := os.WriteFile("/etc/resolv.conf", []byte(renderResolvConf(cfg)), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}

	return nil
}

func networkCommands(cfg *ConfigResponse) []commandSpec {
	cmds := []commandSpec{
		{name: "ip", args: []string{"link", "set", "lo", "up"}},
	}

	if cfg.IPAddr == "" {
		return cmds
	}

	cmds = append(cmds, commandSpec{name: "ip", args: []string{"link", "set", "eth0", "up"}})
	cmds = append(cmds, commandSpec{name: "ip", args: []string{"addr", "add", cfg.IPAddr, "dev", "eth0"}})
	if cfg.IPGW != "" {
		cmds = append(cmds, commandSpec{name: "ip", args: []string{"route", "add", "default", "via", cfg.IPGW}})
	}

	return cmds
}

func renderResolvConf(cfg *ConfigResponse) string {
	servers := cfg.DNSServers
	if len(servers) == 0 {
		servers = []string{"8.8.8.8", "1.1.1.1"}
	}
	var b strings.Builder
	for _, server := range servers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		b.WriteString("nameserver ")
		b.WriteString(server)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		b.WriteString("nameserver 8.8.8.8\n")
		b.WriteString("nameserver 1.1.1.1\n")
	}
	return b.String()
}

func isLocalShellMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "box", "temp":
		return true
	default:
		return false
	}
}

func isRemoteVMGuest(env []string) bool {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) != "KINDLING_REMOTE_VM" {
			continue
		}
		value = strings.TrimSpace(strings.ToLower(value))
		return value != "" && value != "0" && value != "false"
	}
	return false
}

func shouldKeepGuestReadyWithoutApp(cfg *ConfigResponse) bool {
	return isLocalShellMode(cfg.Mode) || isRemoteVMGuest(cfg.Env)
}

func shouldStartHostBridgeWithoutApp(cfg *ConfigResponse) bool {
	return isRemoteVMGuest(cfg.Env)
}

func localRootFSAvailable() bool {
	_, err := os.Stat("/app/bin/sh")
	return err == nil
}

func startLogStream() io.Writer {
	conn, err := dialVsock(vsockCID, configPort)
	if err != nil {
		log.Printf("warning: log stream connect failed: %v", err)
		return os.Stdout
	}

	pr, pw := io.Pipe()
	go func() {
		req, _ := http.NewRequest("POST", "http://localhost/logs", pr)
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return conn, nil
				},
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("log stream error: %v", err)
			return
		}
		resp.Body.Close()
	}()

	return pw
}

func notifyReady() error {
	conn, err := dialVsock(vsockCID, configPort)
	if err != nil {
		return fmt.Errorf("vsock connect: %w", err)
	}
	defer conn.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return conn, nil
			},
		},
	}

	req, err := http.NewRequest(http.MethodPost, "http://localhost/ready", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST /ready: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /ready returned status %d", resp.StatusCode)
	}

	return nil
}

// startHostTCPBridge listens on a vsock port for connections from the hypervisor (Apple VZ host forward)
// and relays each connection to the app on loopback. The host correlates its localhost listener to this port.
func startHostTCPBridge(appPort int) error {
	ln, err := listenVsock(tcpBridgeVsockPort)
	if err != nil {
		return err
	}
	go runHostTCPBridge(ln, appPort)
	return nil
}

func runHostTCPBridge(ln net.Listener, appPort int) {
	appAddr := fmt.Sprintf("127.0.0.1:%d", appPort)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go bridgeGuestConnToApp(c, appAddr)
	}
}

func bridgeGuestConnToApp(guest net.Conn, appAddr string) {
	defer guest.Close()
	back, err := net.Dial("tcp", appAddr)
	if err != nil {
		return
	}
	defer back.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(back, guest)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(guest, back)
		done <- struct{}{}
	}()
	<-done
}

func startApp(env []string, logWriter io.Writer) *exec.Cmd {
	// After chroot, app code is at /app (the container's WORKDIR).
	appDir := "/app"
	if _, err := os.Stat(appDir); err != nil {
		appDir = "/"
	}
	log.Printf("app directory: %s", appDir)

	// Check Procfile first.
	if data, err := os.ReadFile(appDir + "/Procfile"); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "web:") {
				cmdStr := strings.TrimSpace(strings.TrimPrefix(line, "web:"))
				log.Printf("starting from Procfile: %s", cmdStr)
				cmd := exec.Command("sh", "-c", cmdStr)
				cmd.Dir = appDir
				cmd.Env = append(os.Environ(), env...)
				cmd.Stdout = logWriter
				cmd.Stderr = logWriter
				if err := cmd.Start(); err != nil {
					log.Printf("failed to start: %v", err)
					return nil
				}
				return cmd
			}
		}
	}

	// Try common entrypoints.
	entrypoints := []struct {
		check string
		args  []string
	}{
		{"/app/server.js", []string{"node", "server.js"}},
		{"/app/.output/server/index.mjs", []string{"node", ".output/server/index.mjs"}},
		{"/app/package.json", []string{"npm", "start"}},
		{"/app/main", []string{"/app/main"}},
	}

	for _, ep := range entrypoints {
		if _, err := os.Stat(ep.check); err == nil {
			log.Printf("starting app: %v", ep.args)
			cmd := exec.Command(ep.args[0], ep.args[1:]...)
			cmd.Dir = "/app"
			cmd.Env = append(os.Environ(), env...)
			cmd.Stdout = logWriter
			cmd.Stderr = logWriter
			cmd.Start()
			return cmd
		}
	}

	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s: %w", name, args, string(out), err)
	}
	return nil
}

func waitForTCPReady(ctx context.Context, addr string) error {
	dialer := &net.Dialer{Timeout: 200 * time.Millisecond}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
