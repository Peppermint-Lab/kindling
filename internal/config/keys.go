package config

// Cluster setting keys (non-secret, cluster_settings.key).
const (
	SettingRegistryURL                    = "registry_url"
	SettingRegistryUsername               = "registry_username"
	SettingEdgeHTTPSAddr                  = "edge_https_addr"
	SettingEdgeHTTPAddr                   = "edge_http_addr"
	SettingACMEEmail                      = "acme_email"
	SettingACMEStaging                    = "acme_staging"
	SettingColdStartTimeout               = "cold_start_timeout"
	SettingScaleToZeroIdleSeconds         = "scale_to_zero_idle_seconds"
	SettingVolumeBackupS3Bucket           = "volume_backup_s3_bucket"
	SettingVolumeBackupS3Region           = "volume_backup_s3_region"
	SettingVolumeBackupS3Endpoint         = "volume_backup_s3_endpoint"
	SettingVolumeBackupS3Prefix           = "volume_backup_s3_prefix"
	SettingServiceBaseDomain              = "service_base_domain"
	SettingPreviewBaseDomain              = "preview_base_domain"
	SettingPreviewRetentionAfterCloseSecs = "preview_retention_after_close_seconds"
	SettingPreviewIdleSeconds             = "preview_idle_scale_seconds"
)

// Cluster secret keys (cluster_secrets.key, ciphertext).
const (
	SecretGitHubToken                   = "github_token"
	SecretRegistryPassword              = "registry_password"
	SecretVolumeBackupS3AccessKeyID     = "volume_backup_s3_access_key_id"
	SecretVolumeBackupS3SecretAccessKey = "volume_backup_s3_secret_access_key"
)
