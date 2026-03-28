package config

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
)

const defaultColdStartTimeout = 2 * time.Minute // default cold start timeout for edge proxy

// DefaultSnapshot returns defaults used before the first successful Reload.
func DefaultSnapshot() *Snapshot {
	return &Snapshot{
		RegistryURL:                       "kindling",
		EdgeHTTPAddr:                      ":80",
		ColdStartTimeout:                  defaultColdStartTimeout,
		ScaleToZeroIdleSeconds:            300,
		PreviewRetentionAfterCloseSeconds: 3600,
		PreviewIdleSeconds:                300,
	}
}

// Snapshot is an immutable view of runtime configuration loaded from Postgres.
type Snapshot struct {
	GitHubToken                   string
	RegistryURL                   string
	RegistryUsername              string
	RegistryPassword              string
	VolumeBackupS3AccessKeyID     string
	VolumeBackupS3SecretAccessKey string
	VolumeBackupS3Bucket          string
	VolumeBackupS3Region          string
	VolumeBackupS3Endpoint        string
	VolumeBackupS3Prefix          string

	EdgeHTTPSAddr string
	EdgeHTTPAddr  string
	ACMEEmail     string
	ACMEStaging   bool

	ColdStartTimeout                  time.Duration
	ScaleToZeroIdleSeconds            int64
	ServiceBaseDomain                 string
	PreviewBaseDomain                 string
	PreviewRetentionAfterCloseSeconds int64
	PreviewIdleSeconds                int64

	ServerRuntimeOverride              string
	ServerAdvertiseHost                string
	ServerCloudHypervisorBin           string
	ServerCloudHypervisorKernelPath    string
	ServerCloudHypervisorInitramfsPath string
	ServerCloudHypervisorStateDir      string
}

// LoadSnapshot reads cluster_settings, server_settings, and cluster_secrets into a Snapshot.
func LoadSnapshot(ctx context.Context, q *queries.Queries, serverID uuid.UUID, masterKey []byte) (*Snapshot, error) {
	s := &Snapshot{
		RegistryURL:                       "kindling",
		EdgeHTTPAddr:                      ":80",
		ColdStartTimeout:                  defaultColdStartTimeout,
		ScaleToZeroIdleSeconds:            300,
		PreviewRetentionAfterCloseSeconds: 3600,
		PreviewIdleSeconds:                300,
	}

	rows, err := q.ClusterSettingsAll(ctx)
	if err != nil {
		return nil, err
	}
	settings := make(map[string]string, len(rows))
	for _, row := range rows {
		settings[row.Key] = row.Value
	}
	if v := strings.TrimSpace(settings[SettingRegistryURL]); v != "" {
		s.RegistryURL = v
	}
	if v := strings.TrimSpace(settings[SettingRegistryUsername]); v != "" {
		s.RegistryUsername = v
	}
	s.VolumeBackupS3Bucket = strings.TrimSpace(settings[SettingVolumeBackupS3Bucket])
	s.VolumeBackupS3Region = strings.TrimSpace(settings[SettingVolumeBackupS3Region])
	s.VolumeBackupS3Endpoint = strings.TrimSpace(settings[SettingVolumeBackupS3Endpoint])
	s.VolumeBackupS3Prefix = strings.Trim(strings.TrimSpace(settings[SettingVolumeBackupS3Prefix]), "/")
	s.EdgeHTTPSAddr = strings.TrimSpace(settings[SettingEdgeHTTPSAddr])
	s.EdgeHTTPAddr = strings.TrimSpace(settings[SettingEdgeHTTPAddr])
	if s.EdgeHTTPAddr == "" {
		s.EdgeHTTPAddr = ":80"
	}
	s.ACMEEmail = strings.TrimSpace(settings[SettingACMEEmail])
	switch strings.ToLower(strings.TrimSpace(settings[SettingACMEStaging])) {
	case "1", "true", "yes", "on":
		s.ACMEStaging = true
	}
	if v := strings.TrimSpace(settings[SettingColdStartTimeout]); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			s.ColdStartTimeout = d
		}
	}
	if v := strings.TrimSpace(settings[SettingScaleToZeroIdleSeconds]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			s.ScaleToZeroIdleSeconds = n
		}
	}
	s.ServiceBaseDomain = strings.TrimSpace(settings[SettingServiceBaseDomain])
	s.PreviewBaseDomain = strings.TrimSpace(settings[SettingPreviewBaseDomain])
	if v := strings.TrimSpace(settings[SettingPreviewRetentionAfterCloseSecs]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			s.PreviewRetentionAfterCloseSeconds = n
		}
	}
	if v := strings.TrimSpace(settings[SettingPreviewIdleSeconds]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			s.PreviewIdleSeconds = n
		}
	}

	st, err := q.ServerSettingGet(ctx, pgtype.UUID{Bytes: serverID, Valid: true})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	} else {
		s.ServerRuntimeOverride = strings.TrimSpace(st.RuntimeOverride)
		s.ServerAdvertiseHost = strings.TrimSpace(st.AdvertiseHost)
		s.ServerCloudHypervisorBin = strings.TrimSpace(st.CloudHypervisorBin)
		s.ServerCloudHypervisorKernelPath = strings.TrimSpace(st.CloudHypervisorKernelPath)
		s.ServerCloudHypervisorInitramfsPath = strings.TrimSpace(st.CloudHypervisorInitramfsPath)
		s.ServerCloudHypervisorStateDir = strings.TrimSpace(st.CloudHypervisorStateDir)
	}

	if err := decryptSecretInto(ctx, q, masterKey, SecretGitHubToken, &s.GitHubToken); err != nil {
		return nil, err
	}
	if err := decryptSecretInto(ctx, q, masterKey, SecretRegistryPassword, &s.RegistryPassword); err != nil {
		return nil, err
	}
	if err := decryptSecretInto(ctx, q, masterKey, SecretVolumeBackupS3AccessKeyID, &s.VolumeBackupS3AccessKeyID); err != nil {
		return nil, err
	}
	if err := decryptSecretInto(ctx, q, masterKey, SecretVolumeBackupS3SecretAccessKey, &s.VolumeBackupS3SecretAccessKey); err != nil {
		return nil, err
	}

	return s, nil
}

func decryptSecretInto(ctx context.Context, q *queries.Queries, masterKey []byte, key string, dest *string) error {
	ct, err := q.ClusterSecretGet(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			*dest = ""
			return nil
		}
		return fmt.Errorf("get cluster secret %q: %w", key, err)
	}
	if len(ct) == 0 {
		*dest = ""
		return nil
	}
	plain, err := DecryptClusterSecret(masterKey, ct)
	if err != nil {
		return fmt.Errorf("decrypt cluster secret %q: %w", key, err)
	}
	*dest = string(plain)
	return nil
}
