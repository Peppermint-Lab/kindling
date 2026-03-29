//go:build linux

package ci

import (
	"net/http"

	"github.com/kindlingvm/kindling/internal/builder"
)

func newDefaultGitHubRunnerProvisioner() GitHubRunnerProvisioner {
	exec, err := builder.NewCloudHypervisorExecRunner(builder.CloudHypervisorExecRunnerConfig{})
	if err != nil {
		return &unavailableGitHubRunnerProvisioner{
			backend: "github_actions_cloud_hypervisor",
			err:     err,
		}
	}
	return &vmGitHubRunnerProvisioner{
		backend: "github_actions_cloud_hypervisor",
		exec:    exec,
		client:  http.DefaultClient,
	}
}
