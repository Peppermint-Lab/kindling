package bootstrap

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SystemMasterKeyPath holds a 32-byte AES key for cluster_secrets (chmod 600).
	SystemMasterKeyPath = "/etc/kindling/master.key"
	// LocalMasterKeyRelPath is under $HOME for development.
	LocalMasterKeyRelPath = ".kindling/master.key"
)

const masterKeySize = 32

// ErrNoMasterKey indicates the system expects a key at SystemMasterKeyPath but it is missing.
var ErrNoMasterKey = errors.New("master key required at " + SystemMasterKeyPath)

// LoadOrCreateMasterKey returns a 32-byte key used to encrypt cluster secrets at rest.
// Resolution order:
//   1. SystemMasterKeyPath when present and valid length after trim
//   2. $HOME/LocalMasterKeyRelPath when present
//   3. Otherwise generate and write a new key to the home path (development)
func LoadOrCreateMasterKey() ([]byte, error) {
	if raw, err := readKeyFile(SystemMasterKeyPath); err != nil {
		return nil, err
	} else if len(raw) == masterKeySize {
		return raw, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home: %w", err)
	}
	localPath := filepath.Join(home, LocalMasterKeyRelPath)
	if raw, err := readKeyFile(localPath); err != nil {
		return nil, err
	} else if len(raw) == masterKeySize {
		return raw, nil
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir master key dir: %w", err)
	}
	k := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.WriteFile(localPath, k, 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	slog.Warn("auto-generated a new master encryption key — this should be pre-provisioned in production",
		"path", localPath,
		"remediation", "generate a 32-byte key and place it at "+SystemMasterKeyPath+" before starting the server; see docs for key management",
	)
	return k, nil
}

func readKeyFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read master key %s: %w", path, err)
	}
	if len(raw) == masterKeySize {
		return raw, nil
	}
	k := []byte(strings.TrimSpace(string(raw)))
	if len(k) == 0 {
		return nil, nil
	}
	if len(k) != masterKeySize {
		return nil, fmt.Errorf("master key %s: want %d bytes, got %d", path, masterKeySize, len(k))
	}
	return k, nil
}

// LoadMasterKeyProduction reads only the system key (no auto-create). Use on locked-down servers.
func LoadMasterKeyProduction() ([]byte, error) {
	raw, err := readKeyFile(SystemMasterKeyPath)
	if err != nil {
		return nil, err
	}
	if len(raw) != masterKeySize {
		return nil, ErrNoMasterKey
	}
	return raw, nil
}
