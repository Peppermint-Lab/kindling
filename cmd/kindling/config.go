package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kindlingvm/kindling/internal/bootstrap"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/spf13/cobra"
)

func adminConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Bootstrap cluster configuration in Postgres (local/break-glass)",
	}
	cmd.AddCommand(configImportEnvCmd())
	return cmd
}

// configImportEnvCmd reads legacy environment variables once and writes cluster_settings / cluster_secrets.
// Use only for migration; normal operation has no Kindling env vars.
func configImportEnvCmd() *cobra.Command {
	var dbURL string
	cmd := &cobra.Command{
		Use:   "import-env",
		Short: "Copy KINDLING_* / GITHUB_TOKEN from the shell environment into the database (one-time migration)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dsn, err := bootstrap.ResolvePostgresDSN(dbURL)
			if err != nil {
				return fmt.Errorf("resolve postgres DSN: %w", err)
			}
			db, err := database.New(ctx, dsn)
			if err != nil {
				return fmt.Errorf("connect database: %w", err)
			}
			defer db.Close()
			if err := database.Migrate(ctx, db.Pool); err != nil {
				return fmt.Errorf("run migrations: %w", err)
			}

			q := queries.New(db.Pool)
			mk, err := bootstrap.LoadOrCreateMasterKey()
			if err != nil {
				return fmt.Errorf("load master key: %w", err)
			}

			if v := strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_URL")); v != "" {
				if err := q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
					Key: config.SettingRegistryURL, Value: v,
				}); err != nil {
					return fmt.Errorf("upsert registry URL: %w", err)
				}
			}
			if v := strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_USERNAME")); v != "" {
				if err := q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
					Key: config.SettingRegistryUsername, Value: v,
				}); err != nil {
					return fmt.Errorf("upsert registry username: %w", err)
				}
			}
			if v := strings.TrimSpace(os.Getenv("KINDLING_REGISTRY_PASSWORD")); v != "" {
				ct, err := config.EncryptClusterSecret(mk, []byte(v))
				if err != nil {
					return fmt.Errorf("encrypt registry password: %w", err)
				}
				if err := q.ClusterSecretUpsert(ctx, queries.ClusterSecretUpsertParams{
					Key: config.SecretRegistryPassword, Ciphertext: ct,
				}); err != nil {
					return fmt.Errorf("upsert registry password: %w", err)
				}
			}
			if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
				ct, err := config.EncryptClusterSecret(mk, []byte(v))
				if err != nil {
					return fmt.Errorf("encrypt github token: %w", err)
				}
				if err := q.ClusterSecretUpsert(ctx, queries.ClusterSecretUpsertParams{
					Key: config.SecretGitHubToken, Ciphertext: ct,
				}); err != nil {
					return fmt.Errorf("upsert github token: %w", err)
				}
			}

			// Optional edge / operational settings
			setFromEnv := func(key, env string) error {
				v := strings.TrimSpace(os.Getenv(env))
				if v == "" {
					return nil
				}
				return q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{Key: key, Value: v})
			}
			if err := setFromEnv(config.SettingEdgeHTTPSAddr, "KINDLING_EDGE_HTTPS_ADDR"); err != nil {
				return fmt.Errorf("set edge HTTPS addr: %w", err)
			}
			if err := setFromEnv(config.SettingEdgeHTTPAddr, "KINDLING_EDGE_HTTP_ADDR"); err != nil {
				return fmt.Errorf("set edge HTTP addr: %w", err)
			}
			if err := setFromEnv(config.SettingACMEEmail, "KINDLING_ACME_EMAIL"); err != nil {
				return fmt.Errorf("set ACME email: %w", err)
			}
			if strings.TrimSpace(os.Getenv("KINDLING_ACME_STAGING")) != "" {
				if err := q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
					Key: config.SettingACMEStaging, Value: "true",
				}); err != nil {
					return fmt.Errorf("set ACME staging: %w", err)
				}
			}
			if err := setFromEnv(config.SettingColdStartTimeout, "KINDLING_COLD_START_TIMEOUT"); err != nil {
				return fmt.Errorf("set cold start timeout: %w", err)
			}
			if v := strings.TrimSpace(os.Getenv("KINDLING_SCALE_TO_ZERO_IDLE_SECONDS")); v != "" {
				if err := q.ClusterSettingUpsert(ctx, queries.ClusterSettingUpsertParams{
					Key: config.SettingScaleToZeroIdleSeconds, Value: v,
				}); err != nil {
					return fmt.Errorf("set scale-to-zero idle seconds: %w", err)
				}
			}

			fmt.Fprintln(os.Stderr, "import-env done. Unset Kindling-related environment variables for normal runs.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dbURL, "database-url", "", "Optional postgres DSN override (else bootstrap DSN files / default)")
	return cmd
}
