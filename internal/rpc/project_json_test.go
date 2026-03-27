package rpc

import (
	"encoding/json"
	"testing"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestProjectStripSecretIncludesBuildOnlyOnRootChanges(t *testing.T) {
	t.Parallel()

	project := queries.Project{
		GithubWebhookSecret:    "secret",
		BuildOnlyOnRootChanges: true,
	}

	body, err := json.Marshal(projectStripSecret(project))
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}

	if got["github_webhook_secret"] != "" {
		t.Fatalf("expected github_webhook_secret to be stripped, got %#v", got["github_webhook_secret"])
	}
	if got["build_only_on_root_changes"] != true {
		t.Fatalf("expected build_only_on_root_changes to round-trip, got %#v", got["build_only_on_root_changes"])
	}
}
