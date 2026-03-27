package config

// Cluster setting keys (non-secret, cluster_settings.key).
const (
	SettingRegistryURL               = "registry_url"
	SettingRegistryUsername        = "registry_username"
	SettingEdgeHTTPSAddr           = "edge_https_addr"
	SettingEdgeHTTPAddr            = "edge_http_addr"
	SettingACMEEmail               = "acme_email"
	SettingACMEStaging             = "acme_staging"
	SettingColdStartTimeout        = "cold_start_timeout"
	SettingScaleToZeroIdleSeconds  = "scale_to_zero_idle_seconds"
	SettingPreviewBaseDomain              = "preview_base_domain"
	SettingPreviewRetentionAfterCloseSecs = "preview_retention_after_close_seconds"
	SettingPreviewIdleSeconds             = "preview_idle_scale_seconds"
)

// Cluster secret keys (cluster_secrets.key, ciphertext).
const (
	SecretGitHubToken       = "github_token"
	SecretRegistryPassword  = "registry_password"
)
