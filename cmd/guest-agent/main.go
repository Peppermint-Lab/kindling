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
)

const (
	// vsockCID 2 = host from guest perspective.
	vsockCID = 2

	// configPort is the vsock port the host serves config on.
	configPort = 1024
)

// ConfigResponse is the JSON payload from the host's config endpoint.
type ConfigResponse struct {
	Env      []string `json:"env"`
	IPAddr   string   `json:"ip_addr"`
	IPGW     string   `json:"ip_gw"`
	Hostname string   `json:"hostname"`
}

func main() {
	log.SetPrefix("[guest-agent] ")
	log.SetFlags(log.Ltime)

	log.Println("starting guest agent (PID 1)")

	// Mount essential filesystems.
	mountEssentialFS()

	// Fetch config from host via vsock.
	cfg, err := fetchConfig()
	if err != nil {
		log.Fatalf("failed to fetch config: %v", err)
	}
	log.Printf("config received: hostname=%s ip=%s env_count=%d", cfg.Hostname, cfg.IPAddr, len(cfg.Env))

	// Configure networking.
	if err := configureNetwork(cfg); err != nil {
		log.Printf("warning: network config failed: %v", err)
	}

	// Set hostname.
	setHostname(cfg.Hostname)

	// Chroot into the container rootfs if available.
	chrootIntoApp()

	// Start log streaming to host.
	logWriter := startLogStream()

	// Find and start the user's app.
	appCmd := startApp(cfg.Env, logWriter)
	if appCmd == nil {
		log.Println("no application found, idling")
		select {} // block forever
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
	if cfg.IPAddr == "" {
		return nil
	}

	if err := run("ip", "addr", "add", cfg.IPAddr, "dev", "eth0"); err != nil {
		return fmt.Errorf("add addr: %w", err)
	}
	if err := run("ip", "link", "set", "eth0", "up"); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	if cfg.IPGW != "" {
		if err := run("ip", "route", "add", "default", "via", cfg.IPGW); err != nil {
			return fmt.Errorf("default route: %w", err)
		}
	}

	os.MkdirAll("/etc", 0o755)
	os.WriteFile("/etc/resolv.conf", []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0o644)

	return nil
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
