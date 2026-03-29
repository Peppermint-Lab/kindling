package ci

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type cacheStore struct {
	root    string
	entries []cacheEntry
}

type cacheEntry struct {
	Key  string
	Path string
}

func newCacheStore() (*cacheStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".kindling", "ci-cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &cacheStore{root: root}, nil
}

func (c *cacheStore) restore(key, dst string) error {
	src := filepath.Join(c.root, sanitizeName(key))
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	_ = os.RemoveAll(dst)
	return copyPath(src, dst)
}

func (c *cacheStore) track(key, path string) {
	c.entries = append(c.entries, cacheEntry{Key: key, Path: path})
}

func (c *cacheStore) save(key, src string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dst := filepath.Join(c.root, sanitizeName(key))
	_ = os.RemoveAll(dst)
	return copyPath(src, dst)
}

func evaluatePathsFilter(filtersYAML string, changedFiles []string) (map[string]string, error) {
	var parsed map[string][]string
	if err := yaml.Unmarshal([]byte(filtersYAML), &parsed); err != nil {
		return nil, fmt.Errorf("parse paths-filter config: %w", err)
	}
	outputs := make(map[string]string, len(parsed))
	for name, patterns := range parsed {
		match := false
		for _, file := range changedFiles {
			for _, pattern := range patterns {
				ok, err := filepath.Match(pattern, file)
				if err == nil && ok {
					match = true
					break
				}
				if strings.HasSuffix(pattern, "/**") {
					prefix := strings.TrimSuffix(pattern, "/**")
					if strings.HasPrefix(file, strings.TrimPrefix(filepath.ToSlash(prefix), "./")+"/") || file == strings.TrimPrefix(filepath.ToSlash(prefix), "./") {
						match = true
						break
					}
				}
			}
			if match {
				break
			}
		}
		outputs[name] = fmt.Sprintf("%t", match)
	}
	return outputs, nil
}

func startSSHAgent(ctx context.Context, key string, env map[string]string, stdout, stderr io.Writer) (func(), map[string]string, error) {
	if strings.TrimSpace(key) == "" {
		return nil, nil, fmt.Errorf("ssh-private-key is required")
	}
	cmd := exec.CommandContext(ctx, "ssh-agent", "-s")
	cmd.Env = mapToEnv(env)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("start ssh-agent: %w", err)
	}
	agentEnv := parseSSHAgentOutput(out)
	if agentEnv["SSH_AUTH_SOCK"] == "" || agentEnv["SSH_AGENT_PID"] == "" {
		return nil, nil, fmt.Errorf("ssh-agent did not return SSH_AUTH_SOCK and SSH_AGENT_PID")
	}
	add := exec.CommandContext(ctx, "ssh-add", "-")
	add.Env = append(mapToEnv(env), "SSH_AUTH_SOCK="+agentEnv["SSH_AUTH_SOCK"], "SSH_AGENT_PID="+agentEnv["SSH_AGENT_PID"])
	add.Stdin = strings.NewReader(key)
	add.Stdout = stdout
	add.Stderr = stderr
	if err := add.Run(); err != nil {
		return nil, nil, fmt.Errorf("ssh-add: %w", err)
	}
	cleanup := func() {
		kill := exec.Command("ssh-agent", "-k")
		kill.Env = []string{
			"SSH_AUTH_SOCK=" + agentEnv["SSH_AUTH_SOCK"],
			"SSH_AGENT_PID=" + agentEnv["SSH_AGENT_PID"],
		}
		_, _ = kill.CombinedOutput()
	}
	return cleanup, agentEnv, nil
}

func parseSSHAgentOutput(out []byte) map[string]string {
	env := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		for _, key := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID"} {
			if !strings.HasPrefix(line, key+"=") {
				continue
			}
			value := strings.TrimPrefix(line, key+"=")
			if i := strings.IndexByte(value, ';'); i >= 0 {
				value = value[:i]
			}
			env[key] = value
		}
	}
	return env
}
