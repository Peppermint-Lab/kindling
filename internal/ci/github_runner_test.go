package ci

import "testing"

func TestHasRequiredGitHubRunnerLabels(t *testing.T) {
	t.Parallel()

	if !hasRequiredGitHubRunnerLabels([]string{"self-hosted", "kindling", "linux", "x64"}) {
		t.Fatal("expected Kindling runner labels to be accepted")
	}
	if hasRequiredGitHubRunnerLabels([]string{"self-hosted", "linux", "x64"}) {
		t.Fatal("expected missing kindling label to be rejected")
	}
}
