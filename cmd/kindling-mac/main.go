// kindling-mac is the local daemon that manages Linux microVMs on macOS
// via Apple Virtualization Framework.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kindlingvm/kindling/internal/macd"
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
	// Determine config path.
	cfgPath := os.Getenv("KINDLING_MAC_CONFIG")
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".kindling-mac.yaml")
	}

	cfg, err := macd.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
