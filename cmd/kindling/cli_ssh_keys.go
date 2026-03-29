package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func cliSSHKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh-key",
		Short: "Manage SSH keys for sandbox access",
	}
	cmd.AddCommand(cliSSHKeyListCmd())
	cmd.AddCommand(cliSSHKeyAddCmd())
	cmd.AddCommand(cliSSHKeyDeleteCmd())
	return cmd
}

func cliSSHKeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your SSH keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/me/ssh-keys", nil, &out); err != nil {
				return fmt.Errorf("list ssh keys: %w", err)
			}
			if !remoteJSON {
				for _, row := range out {
					fmt.Printf("%s  %-20s  %s\n", jsonFieldString(row, "id"), jsonFieldString(row, "name"), jsonFieldString(row, "public_key"))
				}
				return nil
			}
			return printRemote(out)
		},
	}
}

func cliSSHKeyAddCmd() *cobra.Command {
	var name, filePath, publicKey string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add an SSH public key",
		RunE: func(cmd *cobra.Command, args []string) error {
			key := strings.TrimSpace(publicKey)
			if key == "" {
				if strings.TrimSpace(filePath) == "" {
					return fmt.Errorf("--file or --public-key is required")
				}
				data, err := os.ReadFile(strings.TrimSpace(filePath))
				if err != nil {
					return fmt.Errorf("read key file: %w", err)
				}
				key = strings.TrimSpace(string(data))
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/me/ssh-keys", map[string]any{
				"name":       strings.TrimSpace(name),
				"public_key": key,
			}, &out); err != nil {
				return fmt.Errorf("add ssh key: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Human-readable label")
	cmd.Flags().StringVar(&filePath, "file", "", "Path to a public key file")
	cmd.Flags().StringVar(&publicKey, "public-key", "", "Inline authorized_keys entry")
	return cmd
}

func cliSSHKeyDeleteCmd() *cobra.Command {
	var keyID string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an SSH key by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(keyID)
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			resp, err := c.Do(cmd.Context(), http.MethodDelete, "/api/me/ssh-keys/"+id, nil)
			if err != nil {
				return fmt.Errorf("delete ssh key: %w", err)
			}
			defer resp.Body.Close()
			if err := sandboxResponseError(resp, "delete ssh key"); err != nil {
				return err
			}
			printRemoteMessage("ssh key deleted: " + id)
			return nil
		},
	}
	cmd.Flags().StringVar(&keyID, "id", "", "SSH key UUID")
	return cmd
}
