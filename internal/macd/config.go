// Package macd is the core of the kindling-mac daemon.
// It manages local Linux microVMs on macOS via Apple Virtualization Framework.
package macd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents ~/.kindling-mac.yaml.
type Config struct {
	Box    BoxGroupConfig  `yaml:"box"`
	Temp   TempGroupConfig `yaml:"temp"`
	Daemon DaemonConfig    `yaml:"daemon"`
}

type BoxGroupConfig struct {
	Name          string               `yaml:"name"`
	VCPUs         int                  `yaml:"cpus"`
	MemoryMB      int                  `yaml:"memory_mb"`
	DiskMB        int                  `yaml:"disk_mb"`
	AutoStart     bool                 `yaml:"auto_start"`
	SharedFolders []SharedFolderConfig `yaml:"shared_folders"`
	Rosetta       bool                 `yaml:"rosetta"`
}

type TempGroupConfig struct {
	VCPUs         int                  `yaml:"cpus"`
	MemoryMB      int                  `yaml:"memory_mb"`
	DiskMB        int                  `yaml:"disk_mb"`
	SharedFolders []SharedFolderConfig `yaml:"shared_folders"`
	Rosetta       bool                 `yaml:"rosetta"`
}

type SharedFolderConfig struct {
	HostPath  string `yaml:"host_path"`
	GuestPath string `yaml:"guest_path"`
}

type DaemonConfig struct {
	SocketPath        string `yaml:"socket_path"`
	StateDB           string `yaml:"state_db"`
	GuestAgentPath    string `yaml:"guest_agent_path"`
	KernelPath        string `yaml:"kernel_path"`
	InitramfsPath     string `yaml:"initramfs_path"`
	RootfsArchivePath string `yaml:"rootfs_archive_path"`
}

// DefaultConfig returns a config with sensible defaults for ~/.kindling-mac.yaml.
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	kindlingMacDir := filepath.Join(home, ".kindling-mac")
	return &Config{
		Box: BoxGroupConfig{
			Name:          "box-1",
			VCPUs:         4,
			MemoryMB:      8192,
			DiskMB:        51200,
			AutoStart:     true,
			SharedFolders: []SharedFolderConfig{},
			Rosetta:       false,
		},
		Temp: TempGroupConfig{
			VCPUs:         4,
			MemoryMB:      8192,
			DiskMB:        20480,
			SharedFolders: []SharedFolderConfig{},
			Rosetta:       false,
		},
		Daemon: DaemonConfig{
			SocketPath:        filepath.Join(kindlingMacDir, "kindling-mac.sock"),
			StateDB:           filepath.Join(kindlingMacDir, "state.db"),
			GuestAgentPath:    filepath.Join(kindlingMacDir, "guest-agent"),
			KernelPath:        filepath.Join(kindlingMacDir, "vmlinuz"),
			InitramfsPath:     filepath.Join(kindlingMacDir, "initramfs.cpio.gz"),
			RootfsArchivePath: filepath.Join(kindlingMacDir, "rootfs.tar.gz"),
		},
	}
}

// Load reads and parses ~/.kindling-mac.yaml. Returns DefaultConfig if the file does not exist.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Expand ~ in values.
	expanded := os.ExpandEnv(string(data))
	expanded = expandTilde(expanded)

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if cfg.Daemon.SocketPath == "" {
		cfg.Daemon.SocketPath = filepath.Join(userHomeDir(), ".kindling-mac", "kindling-mac.sock")
	}
	if cfg.Daemon.StateDB == "" {
		cfg.Daemon.StateDB = filepath.Join(userHomeDir(), ".kindling-mac", "state.db")
	}
	if cfg.Daemon.GuestAgentPath == "" {
		cfg.Daemon.GuestAgentPath = filepath.Join(userHomeDir(), ".kindling-mac", "guest-agent")
	}
	if cfg.Daemon.KernelPath == "" {
		cfg.Daemon.KernelPath = filepath.Join(userHomeDir(), ".kindling-mac", "vmlinuz")
	}
	if cfg.Daemon.InitramfsPath == "" {
		cfg.Daemon.InitramfsPath = filepath.Join(userHomeDir(), ".kindling-mac", "initramfs.cpio.gz")
	}
	if cfg.Daemon.RootfsArchivePath == "" {
		cfg.Daemon.RootfsArchivePath = filepath.Join(userHomeDir(), ".kindling-mac", "rootfs.tar.gz")
	}

	return &cfg, nil
}

func expandTilde(s string) string {
	if !strings.Contains(s, "~") {
		return s
	}
	home := userHomeDir()
	return strings.ReplaceAll(s, "~", home)
}

func userHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/tmp"
}

// DaemonDir returns the directory that holds all kindling-mac state.
func DaemonDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kindling-mac")
	}
	return "/tmp/kindling-mac"
}
