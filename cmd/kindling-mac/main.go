// kindling-mac is the local daemon that manages Linux microVMs on macOS
// via Apple Virtualization Framework.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kindlingvm/kindling/internal/macd"
)

const (
	serviceLabel = "com.kindling.kindling-mac"
)

func main() {
	log.SetPrefix("[kindling-mac] ")
	log.SetFlags(log.Ltime)

	if err := run(); err != nil {
		if !strings.Contains(err.Error(), "signal") {
			log.Fatalf("kindling-mac: %v", err)
		}
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) == 0 {
		return startBackground()
	}

	switch args[0] {
	case "run":
		return runForeground()
	case "start":
		return startBackground()
	case "stop":
		return stopBackground()
	case "status":
		return statusBackground()
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText())
	}
}

func runForeground() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}

	// Ensure daemon directory exists.
	daemonDir := macd.DaemonDir()
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return fmt.Errorf("create daemon dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(daemonDir, "vms"), 0755); err != nil {
		return fmt.Errorf("create vms dir: %w", err)
	}

	// Open state store.
	store, err := macd.NewStore(cfg.Daemon.StateDB)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	// Create VM manager.
	mgr := macd.NewManager(cfg, store)

	// Create API server.
	srv, err := macd.NewServer(mgr, cfg.Daemon.SocketPath)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	log.Printf("kindling-mac daemon starting")
	log.Printf("socket: %s", cfg.Daemon.SocketPath)
	log.Printf("state:  %s", cfg.Daemon.StateDB)
	log.Printf("kernel: %s", cfg.Daemon.KernelPath)
	log.Printf("initramfs: %s", cfg.Daemon.InitramfsPath)

	// Start the API server.
	if err := srv.Serve(ctx); err != nil {
		return fmt.Errorf("server: %w", err)
	}

	log.Println("kindling-mac daemon stopped")
	return nil
}

func startBackground() error {
	cfg, cfgPath, err := loadConfig()
	if err != nil {
		return err
	}
	if daemonReachable(cfg.Daemon.SocketPath) {
		fmt.Printf("kindling-mac is already running.\n")
		fmt.Printf("socket: %s\n", cfg.Daemon.SocketPath)
		return nil
	}

	plistPath, logPath, plistChanged, err := installLaunchAgent(cfgPath)
	if err != nil {
		return err
	}

	target := serviceTarget()
	domain := serviceDomain()
	loaded := launchAgentLoaded(target)
	if plistChanged && loaded {
		_ = launchctl("bootout", target)
		loaded = false
	}
	if !loaded {
		if err := launchctl("bootstrap", domain, plistPath); err != nil {
			return err
		}
	}
	if err := launchctl("kickstart", target); err != nil {
		return err
	}
	if daemonReachable(cfg.Daemon.SocketPath) {
		fmt.Printf("kindling-mac started in background.\n")
	} else {
		fmt.Printf("kindling-mac is starting in background.\n")
	}
	fmt.Printf("socket: %s\n", cfg.Daemon.SocketPath)
	fmt.Printf("logs:   %s\n", logPath)
	return nil
}

func stopBackground() error {
	target := serviceTarget()
	if !launchAgentLoaded(target) {
		fmt.Println("kindling-mac is not running.")
		return nil
	}
	if err := launchctl("bootout", target); err != nil {
		return err
	}
	fmt.Println("kindling-mac stopped.")
	return nil
}

func statusBackground() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	loaded := launchAgentLoaded(serviceTarget())
	reachable := daemonReachable(cfg.Daemon.SocketPath)
	status := "stopped"
	if loaded || reachable {
		status = "running"
	}
	fmt.Printf("kindling-mac: %s\n", status)
	fmt.Printf("launch agent loaded: %t\n", loaded)
	fmt.Printf("socket reachable:    %t\n", reachable)
	fmt.Printf("socket:              %s\n", cfg.Daemon.SocketPath)
	return nil
}

func usageText() string {
	return `Usage:
  kindling-mac           Start the background launch agent and return
  kindling-mac start     Start the background launch agent and return
  kindling-mac stop      Stop the background launch agent
  kindling-mac status    Show launch-agent and socket status
  kindling-mac run       Run the daemon in the foreground`
}

func printUsage() {
	fmt.Println(usageText())
}

func loadConfig() (*macd.Config, string, error) {
	cfgPath := os.Getenv("KINDLING_MAC_CONFIG")
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".kindling-mac.yaml")
	}

	cfg, err := macd.LoadConfig(cfgPath)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	return cfg, cfgPath, nil
}

func installLaunchAgent(cfgPath string) (plistPath string, logPath string, changed bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false, fmt.Errorf("resolve home dir: %w", err)
	}
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		return "", "", false, fmt.Errorf("create launch agents dir: %w", err)
	}
	if err := os.MkdirAll(macd.DaemonDir(), 0755); err != nil {
		return "", "", false, fmt.Errorf("create daemon dir: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", "", false, fmt.Errorf("resolve executable path: %w", err)
	}
	if resolvedPath, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolvedPath
	}

	logPath = filepath.Join(macd.DaemonDir(), "kindling-mac.log")
	plistPath = filepath.Join(launchAgentsDir, serviceLabel+".plist")
	contents := servicePlist(execPath, cfgPath, logPath)

	existing, err := os.ReadFile(plistPath)
	if err == nil && string(existing) == contents {
		return plistPath, logPath, false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", "", false, fmt.Errorf("read launch agent: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(contents), 0644); err != nil {
		return "", "", false, fmt.Errorf("write launch agent: %w", err)
	}
	return plistPath, logPath, true, nil
}

func servicePlist(execPath, cfgPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>KINDLING_MAC_CONFIG</key>
		<string>%s</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, serviceLabel, execPath, cfgPath, logPath, logPath)
}

func launchAgentLoaded(target string) bool {
	cmd := exec.Command("launchctl", "print", target)
	return cmd.Run() == nil
}

func serviceDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func serviceTarget() string {
	return fmt.Sprintf("%s/%s", serviceDomain(), serviceLabel)
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("launchctl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func daemonReachable(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
