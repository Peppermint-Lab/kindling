//go:build darwin

package ci

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/kindlingvm/kindling/internal/builder"
)

func newDefaultGitHubRunnerProvisioner() GitHubRunnerProvisioner {
	home, err := os.UserHomeDir()
	if err != nil {
		return &unavailableGitHubRunnerProvisioner{
			backend: "github_actions_apple_vz",
			err:     err,
		}
	}
	exec, err := builder.NewAppleVZExecRunner(builder.AppleVZBuildRunnerConfig{
		KernelPath:       filepath.Join(home, ".kindling", "vmlinuz.bin"),
		InitramfsPath:    filepath.Join(home, ".kindling", "initramfs.cpio.gz"),
		BuilderRootfsDir: filepath.Join(home, ".kindling", "builder-rootfs"),
	})
	if err != nil {
		return &unavailableGitHubRunnerProvisioner{
			backend: "github_actions_apple_vz",
			err:     err,
		}
	}
	return &vmGitHubRunnerProvisioner{
		backend: "github_actions_apple_vz",
		exec:    exec,
		client:  http.DefaultClient,
	}
}
