//go:build !linux && !darwin

package ci

import "fmt"

func newDefaultGitHubRunnerProvisioner() GitHubRunnerProvisioner {
	return &unavailableGitHubRunnerProvisioner{
		backend: "github_actions_unavailable",
		err:     fmt.Errorf("GitHub Actions microVM runners are not supported on this host"),
	}
}
