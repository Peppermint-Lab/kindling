// Package cli holds shared configuration and HTTP helpers for the Kindling remote CLI.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvConfigPath overrides the CLI config file location.
	EnvConfigPath = "KINDLING_CLI_CONFIG"
	// EnvAPIURL default base URL when no profile / flag (e.g. http://127.0.0.1:8080).
	EnvAPIURL = "KINDLING_API_URL"
	// EnvAPIKey optional default API key (Bearer knd_...).
	EnvAPIKey = "KINDLING_API_KEY"
)

const configVersion = 1

// FileConfig is persisted at ~/.kindling/cli-config.json (or KINDLING_CLI_CONFIG).
type FileConfig struct {
	Version         int                `json:"version"`
	CurrentProfile  string             `json:"current_profile"`
	Profiles        map[string]Profile `json:"profiles"`
	LinkedProjectID string             `json:"linked_project_id,omitempty"`
}

// Profile holds one remote target (API base URL + credentials).
type Profile struct {
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key,omitempty"`
	SessionCookie string `json:"session_cookie,omitempty"` // hex-encoded raw session bytes (same as cookie value)
}

// DefaultConfigPath returns ~/.kindling/cli-config.json.
func DefaultConfigPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvConfigPath)); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kindling", "cli-config.json"), nil
}

// LoadFileConfig reads the config file; missing file returns a zero config (not an error).
func LoadFileConfig(path string) (FileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileConfig{
				Version:        configVersion,
				CurrentProfile: "default",
				Profiles:       map[string]Profile{},
			}, nil
		}
		return FileConfig{}, err
	}
	var c FileConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return FileConfig{}, fmt.Errorf("parse cli config: %w", err)
	}
	if c.Version == 0 {
		c.Version = configVersion
	}
	if c.CurrentProfile == "" {
		c.CurrentProfile = "default"
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

// SaveFileConfig writes the config file atomically.
func SaveFileConfig(path string, c FileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	c.Version = configVersion
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return os.Rename(tmp, path)
}

// ResolveProfile returns the effective profile after applying env, file, and flag overrides.
func ResolveProfile(fc FileConfig, profileName, flagBaseURL, flagAPIKey string) (Profile, string, error) {
	name := strings.TrimSpace(profileName)
	if name == "" {
		name = fc.CurrentProfile
	}
	if name == "" {
		name = "default"
	}
	p := fc.Profiles[name]
	if p.BaseURL == "" {
		if v := strings.TrimSpace(os.Getenv(EnvAPIURL)); v != "" {
			p.BaseURL = v
		}
	}
	if p.APIKey == "" {
		if v := strings.TrimSpace(os.Getenv(EnvAPIKey)); v != "" {
			p.APIKey = v
		}
	}
	if flagBaseURL != "" {
		p.BaseURL = flagBaseURL
	}
	if flagAPIKey != "" {
		p.APIKey = flagAPIKey
	}
	p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if p.BaseURL == "" {
		return Profile{}, name, fmt.Errorf("no API base URL: set profile base_url, use --api-url, or %s", EnvAPIURL)
	}
	return p, name, nil
}
