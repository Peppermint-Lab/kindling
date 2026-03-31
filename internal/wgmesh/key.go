package wgmesh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const localWGPrivateKeyRelPath = ".kindling/wg-private-key"

func wgPrivateKeyPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home dir for WireGuard key: %w", err)
	}
	return filepath.Join(home, localWGPrivateKeyRelPath), nil
}

// EnsurePrivateKey loads or generates this node's WireGuard private key using, in order:
// KINDLING_WG_PRIVATE_KEY, ~/.kindling/wg-private-key, else generate a new per-node key.
func EnsurePrivateKey() (wgtypes.Key, error) {
	if s := strings.TrimSpace(os.Getenv("KINDLING_WG_PRIVATE_KEY")); s != "" {
		return wgtypes.ParseKey(s)
	}
	path, err := wgPrivateKeyPath()
	if err != nil {
		return wgtypes.Key{}, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		k, err := wgtypes.ParseKey(strings.TrimSpace(string(raw)))
		if err != nil {
			return wgtypes.Key{}, fmt.Errorf("parse WireGuard private key file %s: %w", path, err)
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return wgtypes.Key{}, fmt.Errorf("read WireGuard private key: %w", err)
	}

	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("generate WireGuard key: %w", err)
	}
	if err := persistWGKeyFile(path, k.String()); err != nil {
		return wgtypes.Key{}, fmt.Errorf("persist WireGuard private key file %s: %w", path, err)
	}
	return k, nil
}

func persistWGKeyFile(path, keyMaterial string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(keyMaterial)+"\n"), 0o600)
}
