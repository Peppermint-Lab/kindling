//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const installerURL = "https://raw.githubusercontent.com/Peppermint-Lab/kindling/main/contrib/install-kindling-mac.sh"

func addPlatformCommands(root *cobra.Command) {
	root.AddCommand(cliLocalCmd())
	root.AddCommand(updateCmd())
}

func updateCmd() *cobra.Command {
	var noRestart bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the local kindling / kindling-mac install on macOS",
		Long: `Download the latest install script, rebuild the local binaries, refresh VM assets,
and restart the local kindling-mac daemon if it was already running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), noRestart)
		},
	}
	cmd.Flags().BoolVar(&noRestart, "no-restart", false, "Do not restart kindling-mac after updating")
	return cmd
}

func runUpdate(ctx context.Context, noRestart bool) error {
	kindlingMacBin, err := kindlingMacBinary()
	if err != nil {
		return err
	}

	wasRunning := false
	if !noRestart {
		wasRunning = daemonRunning(ctx, kindlingMacBin)
		if wasRunning {
			fmt.Println("Stopping kindling-mac before update...")
			if err := runCommand(ctx, kindlingMacBin, "stop"); err != nil {
				return err
			}
		}
	}

	fmt.Println("Downloading latest installer...")
	script, err := fetchInstaller(ctx)
	if err != nil {
		return err
	}

	fmt.Println("Running installer...")
	installCmd := exec.CommandContext(ctx, "bash", "-s", "--")
	installCmd.Stdin = strings.NewReader(script)
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	installCmd.Env = os.Environ()
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("run installer: %w", err)
	}

	if noRestart {
		fmt.Println("Update complete.")
		return nil
	}

	if wasRunning {
		kindlingMacBin, err = kindlingMacBinary()
		if err != nil {
			return err
		}
		fmt.Println("Restarting kindling-mac...")
		if err := runCommand(ctx, kindlingMacBin); err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		api, err := localAPI()
		if err == nil {
			if err := api.waitForSocket(waitCtx); err != nil {
				return fmt.Errorf("restart kindling-mac: %w", err)
			}
		}
		fmt.Println("kindling-mac restarted.")
	} else {
		fmt.Println("Update complete. Start the background daemon with: kindling-mac")
	}

	return nil
}

func fetchInstaller(ctx context.Context) (string, error) {
	url := os.Getenv("KINDLING_UPDATE_INSTALLER_URL")
	if url == "" {
		url = installerURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("download installer: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read installer: %w", err)
	}
	return string(body), nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	if len(out) > 0 {
		fmt.Print(string(out))
	}
	return nil
}

func daemonRunning(ctx context.Context, kindlingMacBin string) bool {
	statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, kindlingMacBin, "status")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "kindling-mac: running")
}
