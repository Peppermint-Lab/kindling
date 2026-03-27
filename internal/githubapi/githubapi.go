// Package githubapi resolves repository refs via the GitHub REST API (for manual
// deploys and update checks when webhooks are unavailable).
package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NormalizeRepo strips URL prefixes and returns owner/repo.
func NormalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	return strings.TrimSpace(repo)
}

type repoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

type commitInfo struct {
	SHA string `json:"sha"`
}

// ResolveCommit returns the full commit SHA for ref. If ref is empty, the
// repository default branch is used. token may be empty for public repos.
func ResolveCommit(ctx context.Context, client *http.Client, token, repo, ref string) (sha, usedRef string, err error) {
	if client == nil {
		client = http.DefaultClient
	}
	repo = NormalizeRepo(repo)
	if repo == "" {
		return "", "", fmt.Errorf("empty repository")
	}

	if ref == "" {
		var info repoInfo
		if err := githubGET(ctx, client, token, fmt.Sprintf("https://api.github.com/repos/%s", repo), &info); err != nil {
			return "", "", err
		}
		if info.DefaultBranch == "" {
			return "", "", fmt.Errorf("could not determine default branch")
		}
		ref = info.DefaultBranch
	}

	var c commitInfo
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", repo, url.PathEscape(ref))
	if err := githubGET(ctx, client, token, apiURL, &c); err != nil {
		return "", "", err
	}
	c.SHA = strings.TrimSpace(c.SHA)
	if c.SHA == "" {
		return "", "", fmt.Errorf("empty commit sha from GitHub")
	}
	return c.SHA, ref, nil
}

func githubGET(ctx context.Context, client *http.Client, token, reqURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create github request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kindling")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("execute github request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}
