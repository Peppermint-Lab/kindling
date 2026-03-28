package rpc

import (
	"encoding/json"
	"testing"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

func TestProjectStripSecretIncludesScalingFields(t *testing.T) {
	t.Parallel()

	project := queries.Project{
		GithubWebhookSecret:    "secret",
		DesiredInstanceCount:   2,
		MinInstanceCount:       0,
		MaxInstanceCount:       4,
		ScaleToZeroEnabled:     true,
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
	if got["min_instance_count"] != float64(0) {
		t.Fatalf("expected min_instance_count to round-trip, got %#v", got["min_instance_count"])
	}
	if got["max_instance_count"] != float64(4) {
		t.Fatalf("expected max_instance_count to round-trip, got %#v", got["max_instance_count"])
	}
	if got["scale_to_zero_enabled"] != true {
		t.Fatalf("expected scale_to_zero_enabled to round-trip, got %#v", got["scale_to_zero_enabled"])
	}
}
