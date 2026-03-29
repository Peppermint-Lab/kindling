package githubactions

import "testing"

func TestResolveRunnerTargetRequiresKindlingLabels(t *testing.T) {
	t.Parallel()

	target, err := ResolveRunnerTarget([]string{"self-hosted", "kindling", "linux", "x64"}, nil)
	if err != nil {
		t.Fatalf("ResolveRunnerTarget returned error: %v", err)
	}
	if target.OS != LabelLinux {
		t.Fatalf("target.OS = %q", target.OS)
	}
	if target.Arch != LabelX64 {
		t.Fatalf("target.Arch = %q", target.Arch)
	}
	if !target.RequireMicroVM {
		t.Fatal("expected microVM execution to be required")
	}
}

func TestResolveRunnerTargetRejectsUnknownLabels(t *testing.T) {
	t.Parallel()

	if _, err := ResolveRunnerTarget([]string{"self-hosted", "kindling", "linux", "x64", "ubuntu-latest"}, nil); err == nil {
		t.Fatal("expected unsupported label error")
	}
}

func TestParseProviderCredentialsSupportsJSONEnvelope(t *testing.T) {
	t.Parallel()

	creds, err := ParseProviderCredentials([]byte(`{"app_private_key_pem":"pem","webhook_secret":"secret"}`))
	if err != nil {
		t.Fatalf("ParseProviderCredentials returned error: %v", err)
	}
	if creds.AppPrivateKeyPEM != "pem" {
		t.Fatalf("AppPrivateKeyPEM = %q", creds.AppPrivateKeyPEM)
	}
	if creds.WebhookSecret != "secret" {
		t.Fatalf("WebhookSecret = %q", creds.WebhookSecret)
	}
}
