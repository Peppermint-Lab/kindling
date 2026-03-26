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
