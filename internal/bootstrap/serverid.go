package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const (
	// LocalServerIDRelPath is resolved under the current user's home directory.
	LocalServerIDRelPath = ".kindling/server-id"
)

// LoadOrCreateServerID reads the persisted server UUID from disk or creates it
// on first boot under the current user's home directory.
func LoadOrCreateServerID() (uuid.UUID, error) {
	path, err := serverIDPath()
	if err != nil {
		return uuid.Nil, err
	}

	raw, err := os.ReadFile(path)
	if err == nil {
		id, err := uuid.Parse(strings.TrimSpace(string(raw)))
		if err != nil {
			return uuid.Nil, fmt.Errorf("corrupt server-id at %s: %w", path, err)
		}
		slog.Info("loaded server ID", "server_id", id, "path", path)
		return id, nil
	}
	if !os.IsNotExist(err) {
		return uuid.Nil, fmt.Errorf("read server-id %s: %w", path, err)
	}

	id := uuid.New()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return uuid.Nil, fmt.Errorf("create server-id dir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(id.String()), 0o644); err != nil {
		return uuid.Nil, fmt.Errorf("write server-id %s: %w", path, err)
	}
	slog.Info("generated server ID", "server_id", id, "path", path)
	return id, nil
}

func serverIDPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve home dir for server-id: %w", err)
	}
	return filepath.Join(home, LocalServerIDRelPath), nil
}
