package oci

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// dockerConfigAuth represents the auth section for a single registry in a Docker config.json.
type dockerConfigAuth struct {
	Auth string `json:"auth"`
}

// dockerConfig is the minimal Docker config.json structure needed for --authfile.
type dockerConfig struct {
	Auths map[string]dockerConfigAuth `json:"auths"`
}

// registryHost extracts the registry host from an image reference.
// For example:
//
//	"ghcr.io/foo/bar:tag" → "ghcr.io"
//	"docker.io/library/alpine:3.19" → "docker.io"
//	"myimage:latest" → "docker.io" (Docker Hub default)
func registryHost(imageRef string) string {
	// Strip docker:// prefix if present.
	imageRef = strings.TrimPrefix(imageRef, "docker://")

	// Split on '/' — if the first segment contains a '.' or ':' it's a registry host.
	parts := strings.SplitN(imageRef, "/", 2)
	if len(parts) == 1 {
		// Short name with no slashes → Docker Hub.
		return "docker.io"
	}
	first := parts[0]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	// Looks like a Docker Hub user/repo.
	return "docker.io"
}

// WriteAuthFile creates a temporary Docker config.json file with credentials for the given
// image reference's registry. The file is created with mode 0600 so only the owner can read it.
// Callers must remove the file after use (defer os.Remove(path)).
//
// The returned path points to a file in the OS temp directory.
func WriteAuthFile(imageRef string, auth *Auth) (path string, err error) {
	if auth == nil || auth.Username == "" {
		return "", fmt.Errorf("auth credentials are required for authfile creation")
	}

	host := registryHost(imageRef)
	encoded := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))

	cfg := dockerConfig{
		Auths: map[string]dockerConfigAuth{
			host: {Auth: encoded},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal auth config: %w", err)
	}

	f, err := os.CreateTemp("", "kindling-auth-*.json")
	if err != nil {
		return "", fmt.Errorf("create auth temp file: %w", err)
	}
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(f.Name())
		}
	}()

	// Set restrictive permissions before writing any data.
	if err = f.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod auth file: %w", err)
	}

	if _, err = f.Write(data); err != nil {
		return "", fmt.Errorf("write auth file: %w", err)
	}

	return f.Name(), nil
}

// redactCredentials replaces any occurrence of auth credentials in a string with [REDACTED].
// This is used to sanitize error messages and log output.
func redactCredentials(s string, auth *Auth) string {
	if auth == nil {
		return s
	}
	if auth.Password != "" {
		s = strings.ReplaceAll(s, auth.Password, "[REDACTED]")
	}
	if auth.Username != "" {
		s = strings.ReplaceAll(s, auth.Username, "[REDACTED]")
	}
	// Also redact the combined user:pass form.
	if auth.Username != "" && auth.Password != "" {
		combined := auth.Username + ":" + auth.Password
		s = strings.ReplaceAll(s, combined, "[REDACTED]")
	}
	return s
}
