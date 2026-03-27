package main

import (
	"fmt"
	"strings"

	"github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

func cliContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage named CLI profiles (API URL + credentials)",
	}
	cmd.AddCommand(cliContextListCmd())
	cmd.AddCommand(cliContextUseCmd())
	return cmd
}

func cliContextListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List profile names and active profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, fc, err := loadFileConfig()
			if err != nil {
				return err
			}
			if remoteJSON {
				out := map[string]any{
					"current_profile": fc.CurrentProfile,
					"profiles":        fc.Profiles,
				}
				return printRemote(out)
			}
			fmt.Printf("current_profile: %s\n", fc.CurrentProfile)
			for name, p := range fc.Profiles {
				authz := "none"
				if strings.TrimSpace(p.APIKey) != "" {
					authz = "api_key"
				} else if strings.TrimSpace(p.SessionCookie) != "" {
					authz = "session"
				}
				fmt.Printf("  %-15s base_url=%s auth=%s\n", name, p.BaseURL, authz)
			}
			return nil
		},
	}
}

func cliContextUseCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "use",
		Short: "Select or create a profile (writes ~/.kindling/cli-config.json)",
		Long: `Set current_profile and merge --api-url / --api-key into the named profile.

Examples:
  kindling context use --profile prod --api-url https://kindling.example.com
  kindling context use --profile prod --api-key knd_...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--profile is required\n  kindling context use --profile default --api-url http://127.0.0.1:8080")
			}
			path, fc, err := loadFileConfig()
			if err != nil {
				return err
			}
			prof := fc.Profiles[name]
			if remoteAPIURL != "" {
				prof.BaseURL = strings.TrimRight(strings.TrimSpace(remoteAPIURL), "/")
			}
			if remoteAPIKey != "" {
				prof.APIKey = strings.TrimSpace(remoteAPIKey)
				prof.SessionCookie = ""
			}
			if fc.Profiles == nil {
				fc.Profiles = map[string]cli.Profile{}
			}
			fc.Profiles[name] = prof
			fc.CurrentProfile = name
			if err := cli.SaveFileConfig(path, fc); err != nil {
				return err
			}
			printRemoteMessage("context: active profile is now " + name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "profile", "", "Profile name (required)")
	return cmd
}
