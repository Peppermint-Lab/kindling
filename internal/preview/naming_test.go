package preview

import "testing"

func TestProductionServiceHostname(t *testing.T) {
	t.Parallel()

	got := ProductionServiceHostname("web-api", "Acme Console", "apps.kindling.test")
	want := "web-api-acme-console.apps.kindling.test"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPreviewHostnamesIncludeServiceSlug(t *testing.T) {
	t.Parallel()

	stable := StableHostname(42, "web-api", "Acme Console", "preview.kindling.test")
	if stable != "pr-42-web-api-acme-console.preview.kindling.test" {
		t.Fatalf("stable hostname = %q", stable)
	}

	immutable := ImmutableHostname("abc12345", 42, "web-api", "Acme Console", "preview.kindling.test")
	if immutable != "abc1234-pr42-web-api-acme-console.preview.kindling.test" {
		t.Fatalf("immutable hostname = %q", immutable)
	}
}
