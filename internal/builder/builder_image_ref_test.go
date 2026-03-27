package builder

import (
	"strings"
	"testing"

	"github.com/kindlingvm/kindling/internal/githubapi"
)

func TestImageRefUsesLowercaseOCIRepository(t *testing.T) {
	githubRepo := "Peppermint-Lab/kindling"
	ociRepo := strings.ToLower(githubapi.NormalizeRepo(githubRepo))
	if ociRepo != "peppermint-lab/kindling" {
		t.Fatalf("oci repo = %q", ociRepo)
	}
}

func TestNormalizeRegistryURLDefaultsToKindling(t *testing.T) {
	if got := normalizeRegistryURL(""); got != "kindling" {
		t.Fatalf("normalizeRegistryURL(\"\") = %q, want %q", got, "kindling")
	}
	if got := normalizeRegistryURL("   "); got != "kindling" {
		t.Fatalf("normalizeRegistryURL(blank) = %q, want %q", got, "kindling")
	}
	if got := normalizeRegistryURL("registry.example"); got != "registry.example" {
		t.Fatalf("normalizeRegistryURL(custom) = %q", got)
	}
}
