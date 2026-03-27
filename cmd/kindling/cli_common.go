package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

var (
	remoteJSON    bool
	remoteProfile string
	remoteAPIURL  string
	remoteAPIKey  string
)

func remotePersistentFlags(c *cobra.Command) {
	c.PersistentFlags().BoolVar(&remoteJSON, "json", false, "Emit JSON on stdout for agents and scripts")
	c.PersistentFlags().StringVar(&remoteProfile, "profile", "", "Named profile in ~/.kindling/cli-config.json (default: current_profile)")
	c.PersistentFlags().StringVar(&remoteAPIURL, "api-url", "", "API base URL override (e.g. http://127.0.0.1:8080 or https://kindling.example.com)")
	c.PersistentFlags().StringVar(&remoteAPIKey, "api-key", "", "API key override (Bearer knd_...); else profile or KINDLING_API_KEY")
}

func mustConfigPath() (string, error) {
	return cli.DefaultConfigPath()
}

func loadFileConfig() (string, cli.FileConfig, error) {
	path, err := mustConfigPath()
	if err != nil {
		return "", cli.FileConfig{}, err
	}
	fc, err := cli.LoadFileConfig(path)
	return path, fc, err
}

func mustRemoteClient() (*cli.Client, error) {
	_, fc, err := loadFileConfig()
	if err != nil {
		return nil, err
	}
	p, _, err := cli.ResolveProfile(fc, remoteProfile, remoteAPIURL, remoteAPIKey)
	if err != nil {
		return nil, err
	}
	return cli.NewClient(p)
}

func printRemote(v any) error {
	if remoteJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	if s, ok := v.(string); ok {
		fmt.Println(s)
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printRemoteMessage(msg string) {
	if remoteJSON {
		_ = printRemote(map[string]string{"message": msg})
		return
	}
	fmt.Println(msg)
}
