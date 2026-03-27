package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

func cliAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate to the remote Kindling API",
		Long:  "Log in with email/password (session cookie saved to CLI config) or manage API keys after you are authenticated.",
	}
	cmd.AddCommand(cliAuthLoginCmd())
	cmd.AddCommand(cliAuthLogoutCmd())
	cmd.AddCommand(cliAuthWhoamiCmd())
	cmd.AddCommand(cliAuthAPIKeyCmd())
	return cmd
}

func cliAuthLoginCmd() *cobra.Command {
	var email, password string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with email and password (stores session in CLI profile)",
		Long: `POST /api/auth/login and persist the session cookie on the active profile.

Examples:
  kindling auth login --email you@example.com --password '...'
  kindling auth login --email you@example.com --api-url http://127.0.0.1:8080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, fc, err := loadFileConfig()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(remoteProfile)
			if name == "" {
				name = fc.CurrentProfile
			}
			if name == "" {
				name = "default"
			}
			p, _, err := cli.ResolveProfile(fc, name, remoteAPIURL, "")
			if err != nil {
				return err
			}
			if strings.TrimSpace(email) == "" {
				return fmt.Errorf("--email is required\n  kindling auth login --email you@example.com --password ...")
			}
			if password == "" {
				b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
				if err != nil {
					return err
				}
				password = strings.TrimSpace(string(b))
				if password == "" {
					return fmt.Errorf("password is empty: pass --password or pipe stdin\n  kindling auth login --email you@example.com --password '...'")
				}
			}
			body, _ := json.Marshal(map[string]string{
				"email":    strings.TrimSpace(strings.ToLower(email)),
				"password": password,
			})
			u := strings.TrimRight(p.BaseURL, "/") + "/api/auth/login"
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, u, bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
			}
			hexVal, err := cli.SessionHexFromLoginResponse(resp)
			if err != nil {
				return err
			}
			prof := fc.Profiles[name]
			prof.BaseURL = p.BaseURL
			prof.SessionCookie = hexVal
			prof.APIKey = ""
			if fc.Profiles == nil {
				fc.Profiles = map[string]cli.Profile{}
			}
			fc.Profiles[name] = prof
			fc.CurrentProfile = name
			if err := cli.SaveFileConfig(path, fc); err != nil {
				return err
			}
			printRemoteMessage("logged in; session saved to profile " + name)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Login email (required)")
	cmd.Flags().StringVar(&password, "password", "", "Password (omit to read first line from stdin)")
	return cmd
}

func cliAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear saved session (and revoke server session when possible)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, fc, err := loadFileConfig()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(remoteProfile)
			if name == "" {
				name = fc.CurrentProfile
			}
			if name == "" {
				name = "default"
			}
			prof := fc.Profiles[name]
			base := strings.TrimRight(strings.TrimSpace(prof.BaseURL), "/")
			if remoteAPIURL != "" {
				base = strings.TrimRight(strings.TrimSpace(remoteAPIURL), "/")
			}
			if base != "" && strings.TrimSpace(prof.SessionCookie) != "" {
				c, _ := cli.NewClient(cli.Profile{BaseURL: base, SessionCookie: prof.SessionCookie})
				if c != nil {
					_, _ = c.Do(cmd.Context(), http.MethodPost, "/api/auth/logout", nil)
				}
			}
			prof.SessionCookie = ""
			if fc.Profiles == nil {
				fc.Profiles = map[string]cli.Profile{}
			}
			fc.Profiles[name] = prof
			if err := cli.SaveFileConfig(path, fc); err != nil {
				return err
			}
			printRemoteMessage("logged out (local session cleared for profile " + name + ")")
			return nil
		},
	}
}

func cliAuthWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print current session / identity from GET /api/auth/session",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/auth/session", nil, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
}

func cliAuthAPIKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api-key",
		Short: "List, create, or revoke API keys (requires org admin)",
	}
	cmd.AddCommand(cliAuthAPIKeyListCmd())
	cmd.AddCommand(cliAuthAPIKeyCreateCmd())
	cmd.AddCommand(cliAuthAPIKeyRevokeCmd())
	return cmd
}

func cliAuthAPIKeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List API keys for the current organization",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/auth/api-keys", nil, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
}

func cliAuthAPIKeyCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an API key (token printed once)",
		Example: `  kindling auth api-key create --name ci
  kindling auth api-key create --name laptop --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required\n  kindling auth api-key create --name my-key")
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/auth/api-keys", map[string]string{"name": strings.TrimSpace(name)}, &out); err != nil {
				return err
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Human-readable key name (required)")
	return cmd
}

func cliAuthAPIKeyRevokeCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke an API key by id (from list)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("--id is required\n  kindling auth api-key list --json\n  kindling auth api-key revoke --id <uuid>")
			}
			c, err := mustRemoteClient()
			if err != nil {
				return err
			}
			var out map[string]any
			err = c.DoJSON(cmd.Context(), http.MethodDelete, "/api/auth/api-keys/"+strings.TrimSpace(id), nil, &out)
			if err != nil {
				return err
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "API key id (UUID)")
	return cmd
}
