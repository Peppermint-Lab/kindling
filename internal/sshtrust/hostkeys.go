package sshtrust

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

func NormalizeAuthorizedKey(publicKey string) (string, error) {
	trimmed := strings.TrimSpace(publicKey)
	if trimmed == "" {
		return "", nil
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

func ExtractMarkedAuthorizedKey(output, prefix string) (string, error) {
	var candidate string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			candidate = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	if candidate == "" {
		return "", nil
	}
	return NormalizeAuthorizedKey(candidate)
}

func KnownHostsLine(hostAlias, publicKey string) (string, error) {
	alias := strings.TrimSpace(hostAlias)
	if alias == "" {
		return "", fmt.Errorf("host alias is required")
	}
	key, err := NormalizeAuthorizedKey(publicKey)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", fmt.Errorf("public key is required")
	}
	return alias + " " + key + "\n", nil
}
