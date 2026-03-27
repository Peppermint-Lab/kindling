package rpc

import "testing"

func TestNormalizeOrgSlug(t *testing.T) {
	t.Parallel()

	if got := normalizeOrgSlug("Jane Doe Workspace"); got != "jane-doe-workspace" {
		t.Fatalf("normalizeOrgSlug(spaces) = %q", got)
	}
	if got := normalizeOrgSlug("__Team@@123__"); got != "team123" {
		t.Fatalf("normalizeOrgSlug(symbols) = %q", got)
	}
	if got := normalizeOrgSlug("  "); got != "" {
		t.Fatalf("normalizeOrgSlug(blank) = %q", got)
	}
}
