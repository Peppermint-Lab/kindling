package bootstrap

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureSlog installs a JSON slog handler that writes to a buffer and returns
// the buffer plus a cleanup function that restores the previous default logger.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// ---------------------------------------------------------------------------
// VAL-OPSEC-001: Startup warns on default Postgres DSN fallback
// ---------------------------------------------------------------------------

func TestResolvePostgresDSN_DefaultFallback_EmitsWarning(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists; default fallback would not trigger")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	buf := captureSlog(t)

	dsn, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dsn != DefaultLocalPostgresDSN {
		t.Fatalf("expected default DSN, got %q", dsn)
	}

	logs := buf.String()
	if !strings.Contains(logs, "using default Postgres DSN with well-known credentials") {
		t.Fatalf("expected default-DSN warning in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, SystemDSNPath) {
		t.Fatalf("expected remediation mentioning %s in logs, got:\n%s", SystemDSNPath, logs)
	}
	if !strings.Contains(logs, "WARN") {
		t.Fatalf("expected WARN level in logs, got:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// VAL-OPSEC-003: Startup warns when database DSN disables TLS
// ---------------------------------------------------------------------------

func TestResolvePostgresDSN_SSLModeDisable_EmitsWarning(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists; would take precedence")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Write a DSN file that has sslmode=disable.
	p := filepath.Join(dir, LocalDSNRelPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	dsn := "postgres://prod:secret@db.example.com:5432/kindling?sslmode=disable"
	if err := os.WriteFile(p, []byte(dsn), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := captureSlog(t)

	got, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dsn {
		t.Fatalf("expected DSN from file, got %q", got)
	}

	logs := buf.String()
	if !strings.Contains(logs, "TLS disabled") {
		t.Fatalf("expected sslmode=disable warning in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, "sslmode=require") || !strings.Contains(logs, "sslmode=verify-full") {
		t.Fatalf("expected TLS remediation in logs, got:\n%s", logs)
	}
}

// DefaultLocalPostgresDSN contains sslmode=disable, so the default fallback
// should also emit the TLS warning in addition to the credentials warning.
func TestResolvePostgresDSN_DefaultFallback_EmitsBothWarnings(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	buf := captureSlog(t)

	_, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "well-known credentials") {
		t.Fatalf("missing default-DSN credentials warning, got:\n%s", logs)
	}
	if !strings.Contains(logs, "TLS disabled") {
		t.Fatalf("missing sslmode=disable warning, got:\n%s", logs)
	}
}

// When a DSN is loaded from a file with TLS enabled, no sslmode warning fires.
func TestResolvePostgresDSN_SSLModeEnabled_NoWarning(t *testing.T) {
	if _, err := os.Stat(SystemDSNPath); err == nil {
		t.Skip("system DSN file exists")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	p := filepath.Join(dir, LocalDSNRelPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	dsn := "postgres://prod:secret@db.example.com:5432/kindling?sslmode=require"
	if err := os.WriteFile(p, []byte(dsn), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := captureSlog(t)

	got, err := ResolvePostgresDSN("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dsn {
		t.Fatalf("unexpected DSN %q", got)
	}

	logs := buf.String()
	if strings.Contains(logs, "TLS disabled") {
		t.Fatalf("unexpected sslmode=disable warning when TLS is enabled:\n%s", logs)
	}
	if strings.Contains(logs, "well-known credentials") {
		t.Fatalf("unexpected default-DSN warning when file was provided:\n%s", logs)
	}
}

// CLI override also checks sslmode.
func TestResolvePostgresDSN_OverrideSSLModeDisable_EmitsWarning(t *testing.T) {
	buf := captureSlog(t)

	dsn := "postgres://user:pass@host/db?sslmode=disable"
	got, err := ResolvePostgresDSN(dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dsn {
		t.Fatalf("unexpected DSN %q", got)
	}

	logs := buf.String()
	if !strings.Contains(logs, "TLS disabled") {
		t.Fatalf("expected sslmode=disable warning for CLI override, got:\n%s", logs)
	}
}

// CLI override with TLS enabled emits no warning.
func TestResolvePostgresDSN_OverrideSSLModeEnabled_NoWarning(t *testing.T) {
	buf := captureSlog(t)

	dsn := "postgres://user:pass@host/db?sslmode=verify-full"
	_, err := ResolvePostgresDSN(dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, "TLS disabled") {
		t.Fatalf("unexpected sslmode warning for TLS-enabled override:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// VAL-OPSEC-002: Startup warns on auto-generated master key
// ---------------------------------------------------------------------------

func TestLoadOrCreateMasterKey_AutoGenerate_EmitsWarning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	buf := captureSlog(t)

	key, err := LoadOrCreateMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != masterKeySize {
		t.Fatalf("expected %d-byte key, got %d", masterKeySize, len(key))
	}

	logs := buf.String()
	if !strings.Contains(logs, "auto-generated a new master encryption key") {
		t.Fatalf("expected auto-generate warning in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, SystemMasterKeyPath) {
		t.Fatalf("expected remediation mentioning %s in logs, got:\n%s", SystemMasterKeyPath, logs)
	}
	if !strings.Contains(logs, "WARN") {
		t.Fatalf("expected WARN level in logs, got:\n%s", logs)
	}
}

// When the key already exists on disk, no warning should be emitted.
func TestLoadOrCreateMasterKey_ExistingKey_NoWarning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Pre-create a valid master key file.
	localPath := filepath.Join(dir, LocalMasterKeyRelPath)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existingKey := bytes.Repeat([]byte{0xAB}, masterKeySize)
	if err := os.WriteFile(localPath, existingKey, 0o600); err != nil {
		t.Fatal(err)
	}

	buf := captureSlog(t)

	key, err := LoadOrCreateMasterKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(key, existingKey) {
		t.Fatal("expected existing key to be loaded, not a new one")
	}

	logs := buf.String()
	if strings.Contains(logs, "auto-generated") {
		t.Fatalf("unexpected auto-generate warning when key exists:\n%s", logs)
	}
}
