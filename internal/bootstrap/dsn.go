// Package bootstrap resolves Postgres connectivity and host-held secrets keys without
// process environment variables. Installers or dev setups write DSN files; see ResolvePostgresDSN.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SystemDSNPath is the default path provisioned on servers (single line: postgres connection URI).
	SystemDSNPath = "/etc/kindling/postgres.dsn"
	// LocalDSNRelPath is resolved under the current user's home directory.
	LocalDSNRelPath = ".kindling/postgres.dsn"
)

// DefaultLocalPostgresDSN matches contrib/dev-postgres.sh and Makefile local defaults when no DSN file exists.
const DefaultLocalPostgresDSN = "postgres://kindling:kindling@127.0.0.1:5432/kindling?sslmode=disable"

// ResolvePostgresDSN returns a connection URI by reading, in order:
//   - path if non-empty (CLI override for one-shot tools)
//   - SystemDSNPath when the file exists and is non-empty
//   - $HOME/LocalDSNRelPath when the file exists and is non-empty
//   - DefaultLocalPostgresDSN
//
// Whitespace around the DSN is trimmed. Empty files are treated as missing.
func ResolvePostgresDSN(pathOverride string) (string, error) {
	if s := strings.TrimSpace(pathOverride); s != "" {
		return s, nil
	}
	for _, p := range dsnSearchPaths() {
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read postgres dsn file %s: %w", p, err)
		}
		s := strings.TrimSpace(string(raw))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		return s, nil
	}
	return DefaultLocalPostgresDSN, nil
}

func dsnSearchPaths() []string {
	var out []string
	if p := strings.TrimSpace(SystemDSNPath); p != "" {
		out = append(out, p)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, LocalDSNRelPath))
	}
	return out
}
