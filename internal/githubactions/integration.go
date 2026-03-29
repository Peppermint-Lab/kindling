package githubactions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kindlingvm/kindling/internal/database/queries"
)

const (
	ProviderModeActionsRunner = "actions_runner"
	LabelSelfHosted           = "self-hosted"
	LabelKindling             = "kindling"
	LabelLinux                = "linux"
	LabelX64                  = "x64"
	LabelARM64                = "arm64"
	LabelMicroVM              = "microvm"
	LabelLarge                = "large"
)

type ProviderMetadata struct {
	Mode           string   `json:"mode,omitempty"`
	OrgLogin       string   `json:"org_login,omitempty"`
	AppID          int64    `json:"app_id,omitempty"`
	InstallationID int64    `json:"installation_id,omitempty"`
	RunnerGroupID  int64    `json:"runner_group_id,omitempty"`
	DefaultLabels  []string `json:"default_labels,omitempty"`
}

type ProviderCredentials struct {
	AppPrivateKeyPEM string `json:"app_private_key_pem,omitempty"`
	WebhookSecret    string `json:"webhook_secret,omitempty"`
}

type Integration struct {
	Connection  queries.OrgProviderConnection
	Metadata    ProviderMetadata
	Credentials ProviderCredentials
}

type RunnerTarget struct {
	Labels         []string
	OS             string
	Arch           string
	RequireMicroVM bool
	Size           string
}

func ParseProviderMetadata(raw []byte) (ProviderMetadata, error) {
	if len(raw) == 0 {
		return ProviderMetadata{}, nil
	}
	var meta ProviderMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ProviderMetadata{}, fmt.Errorf("parse GitHub Actions metadata: %w", err)
	}
	meta.Mode = strings.TrimSpace(strings.ToLower(meta.Mode))
	meta.OrgLogin = strings.TrimSpace(meta.OrgLogin)
	meta.DefaultLabels = normalizeLabels(meta.DefaultLabels)
	return meta, nil
}

func ParseProviderCredentials(raw []byte) (ProviderCredentials, error) {
	if len(raw) == 0 {
		return ProviderCredentials{}, fmt.Errorf("missing GitHub Actions credentials")
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ProviderCredentials{}, fmt.Errorf("missing GitHub Actions credentials")
	}

	var creds ProviderCredentials
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &creds); err != nil {
			return ProviderCredentials{}, fmt.Errorf("parse GitHub Actions credentials: %w", err)
		}
	} else {
		creds.AppPrivateKeyPEM = trimmed
	}
	creds.AppPrivateKeyPEM = strings.TrimSpace(creds.AppPrivateKeyPEM)
	creds.WebhookSecret = strings.TrimSpace(creds.WebhookSecret)
	if creds.AppPrivateKeyPEM == "" {
		return ProviderCredentials{}, fmt.Errorf("GitHub Actions credentials are missing app_private_key_pem")
	}
	return creds, nil
}

func IntegrationFromConnection(conn queries.OrgProviderConnection, decryptedCredentials []byte) (Integration, error) {
	meta, err := ParseProviderMetadata(conn.Metadata)
	if err != nil {
		return Integration{}, err
	}
	if meta.Mode != ProviderModeActionsRunner {
		return Integration{}, fmt.Errorf("provider connection is not configured for GitHub Actions runners")
	}
	creds, err := ParseProviderCredentials(decryptedCredentials)
	if err != nil {
		return Integration{}, err
	}
	if strings.TrimSpace(meta.OrgLogin) == "" {
		return Integration{}, fmt.Errorf("GitHub Actions metadata is missing org_login")
	}
	if meta.AppID <= 0 {
		return Integration{}, fmt.Errorf("GitHub Actions metadata is missing app_id")
	}
	if meta.InstallationID <= 0 {
		return Integration{}, fmt.Errorf("GitHub Actions metadata is missing installation_id")
	}
	return Integration{
		Connection:  conn,
		Metadata:    meta,
		Credentials: creds,
	}, nil
}

func ResolveIntegrationForOwner(integrations []Integration, owner string) (Integration, bool) {
	owner = strings.TrimSpace(strings.ToLower(owner))
	if owner == "" {
		return Integration{}, false
	}
	for _, integration := range integrations {
		if strings.EqualFold(strings.TrimSpace(integration.Metadata.OrgLogin), owner) ||
			strings.EqualFold(strings.TrimSpace(integration.Connection.ExternalSlug), owner) {
			return integration, true
		}
	}
	return Integration{}, false
}

func ResolveRunnerTarget(rawLabels, defaultLabels []string) (RunnerTarget, error) {
	all := normalizeLabels(append(append([]string(nil), defaultLabels...), rawLabels...))
	seen := map[string]bool{}
	target := RunnerTarget{
		Labels:         all,
		OS:             LabelLinux,
		Arch:           LabelX64,
		RequireMicroVM: true,
	}

	for _, label := range all {
		seen[label] = true
		switch label {
		case LabelSelfHosted, LabelKindling, LabelLinux, LabelMicroVM:
		case LabelX64:
			target.Arch = LabelX64
		case LabelARM64:
			target.Arch = LabelARM64
		case LabelLarge:
			target.Size = LabelLarge
		default:
			return RunnerTarget{}, fmt.Errorf("unsupported Kindling runner label %q", label)
		}
	}

	if !seen[LabelSelfHosted] {
		return RunnerTarget{}, fmt.Errorf("GitHub Actions job must include %q label", LabelSelfHosted)
	}
	if !seen[LabelKindling] {
		return RunnerTarget{}, fmt.Errorf("GitHub Actions job must include %q label", LabelKindling)
	}
	if target.OS != LabelLinux {
		return RunnerTarget{}, fmt.Errorf("unsupported runner operating system %q", target.OS)
	}
	return target, nil
}

func normalizeLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(strings.ToLower(label))
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}
