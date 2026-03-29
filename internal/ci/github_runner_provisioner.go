package ci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kindlingvm/kindling/internal/builder"
	"github.com/kindlingvm/kindling/internal/githubactions"
)

type execRunner interface {
	Exec(context.Context, builder.ExecRun) (int, error)
}

type vmGitHubRunnerProvisioner struct {
	backend string
	exec    execRunner
	client  *http.Client
}

type unavailableGitHubRunnerProvisioner struct {
	backend string
	err     error
}

func (p *vmGitHubRunnerProvisioner) Backend() string {
	return p.backend
}

func (p *vmGitHubRunnerProvisioner) SupportsTarget(target githubactions.RunnerTarget) error {
	if target.OS != githubactions.LabelLinux {
		return fmt.Errorf("unsupported GitHub runner operating system %q", target.OS)
	}
	if target.Arch != githubactions.LabelX64 {
		return fmt.Errorf("unsupported GitHub runner architecture %q", target.Arch)
	}
	if target.Size != "" && target.Size != githubactions.LabelLarge {
		return fmt.Errorf("unsupported GitHub runner size %q", target.Size)
	}
	if target.Size == githubactions.LabelLarge {
		return fmt.Errorf("unsupported GitHub runner label %q", githubactions.LabelLarge)
	}
	if !target.RequireMicroVM {
		return fmt.Errorf("GitHub runners must use microVM execution")
	}
	return nil
}

func (p *vmGitHubRunnerProvisioner) Run(ctx context.Context, req GitHubRunnerProvisionRequest) error {
	root, err := os.MkdirTemp("", "kindling-gh-runner-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	if req.LogLine != nil {
		req.LogLine(fmt.Sprintf("Downloading GitHub Actions runner for labels: %s", strings.Join(req.Target.Labels, ", ")))
	}
	runnerDir, err := p.prepareRunnerDir(ctx, root, req.Target)
	if err != nil {
		return err
	}

	script := `
set -euo pipefail
cd /workspace/runner
chmod +x ./config.sh ./run.sh || true
./config.sh --jitconfig "$KINDLING_GITHUB_JIT_CONFIG" --unattended --disableupdate
./run.sh
`
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin",
		"HOME=/workspace",
		"RUNNER_ALLOW_RUNASROOT=1",
		"KINDLING_GITHUB_JIT_CONFIG=" + req.JITConfig.EncodedJITConfig,
	}
	code, err := p.exec.Exec(ctx, builder.ExecRun{
		WorkspaceDir: root,
		Cwd:          "/workspace/runner",
		Argv:         []string{"sh", "-lc", script},
		Env:          env,
		LogLine:      req.LogLine,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("GitHub runner exited with code %d", code)
	}
	if runnerDir == "" {
		return fmt.Errorf("runner directory was not prepared")
	}
	return nil
}

func (p *vmGitHubRunnerProvisioner) prepareRunnerDir(ctx context.Context, root string, target githubactions.RunnerTarget) (string, error) {
	version, downloadURL, err := resolveGitHubRunnerDownload(ctx, p.client, target)
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(root, "runner.tar.gz")
	if err := downloadToFile(ctx, p.client, downloadURL, archivePath); err != nil {
		return "", err
	}
	runnerDir := filepath.Join(root, "runner")
	if err := os.MkdirAll(runnerDir, 0o755); err != nil {
		return "", err
	}
	if err := extractTarGz(archivePath, runnerDir); err != nil {
		return "", err
	}
	_ = os.Remove(archivePath)
	_ = os.WriteFile(filepath.Join(root, ".runner-version"), []byte(version+"\n"), 0o644)
	return runnerDir, nil
}

func (p *unavailableGitHubRunnerProvisioner) Backend() string {
	if strings.TrimSpace(p.backend) == "" {
		return "github_actions_unavailable"
	}
	return p.backend
}

func (p *unavailableGitHubRunnerProvisioner) SupportsTarget(target githubactions.RunnerTarget) error {
	if p.err != nil {
		return p.err
	}
	return fmt.Errorf("GitHub runner provisioner is unavailable")
}

func (p *unavailableGitHubRunnerProvisioner) Run(ctx context.Context, req GitHubRunnerProvisionRequest) error {
	if p.err != nil {
		return p.err
	}
	return fmt.Errorf("GitHub runner provisioner is unavailable")
}

func resolveGitHubRunnerDownload(ctx context.Context, client *http.Client, target githubactions.RunnerTarget) (string, string, error) {
	version := strings.TrimPrefix(strings.TrimSpace(os.Getenv("KINDLING_GITHUB_RUNNER_VERSION")), "v")
	if version == "" {
		var err error
		version, err = fetchLatestGitHubRunnerVersion(ctx, client)
		if err != nil {
			return "", "", err
		}
	}
	arch := target.Arch
	if arch == "" {
		arch = githubactions.LabelX64
	}
	url := fmt.Sprintf("https://github.com/actions/runner/releases/download/v%s/actions-runner-linux-%s-%s.tar.gz", version, arch, version)
	return version, url, nil
}

func fetchLatestGitHubRunnerVersion(ctx context.Context, client *http.Client) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/actions/runner/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("create GitHub runner release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kindling")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest GitHub runner release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("latest GitHub runner release %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest GitHub runner release: %w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if version == "" {
		return "", fmt.Errorf("latest GitHub runner release did not include a tag name")
	}
	return version, nil
}

func downloadToFile(ctx context.Context, client *http.Client, rawURL, dst string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("User-Agent", "kindling")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download GitHub runner: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download GitHub runner %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	file, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("write GitHub runner archive: %w", err)
	}
	return nil
}

func extractTarGz(src, dst string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		targetPath := filepath.Join(dst, name)
		absTarget, err := filepath.Abs(targetPath)
		if err != nil {
			return err
		}
		absRoot, err := filepath.Abs(dst)
		if err != nil {
			return err
		}
		if absTarget != absRoot && !strings.HasPrefix(absTarget, absRoot+string(filepath.Separator)) {
			return fmt.Errorf("tar entry escapes destination: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(absTarget, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(absTarget, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}
