package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kindlingvm/kindling/internal/auth"
	kcli "github.com/kindlingvm/kindling/internal/cli"
	"github.com/kindlingvm/kindling/internal/shellwire"
	"github.com/kindlingvm/kindling/internal/sshtrust"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func cliSandboxRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes via the control-plane API",
	}
	cmd.AddCommand(cliSandboxListCmd())
	cmd.AddCommand(cliSandboxGetCmd())
	cmd.AddCommand(cliSandboxCreateCmd())
	cmd.AddCommand(cliSandboxUpdateCmd())
	cmd.AddCommand(cliSandboxDeleteCmd())
	cmd.AddCommand(cliSandboxStartCmd())
	cmd.AddCommand(cliSandboxStopCmd())
	cmd.AddCommand(cliSandboxSuspendCmd())
	cmd.AddCommand(cliSandboxResumeCmd())
	cmd.AddCommand(cliSandboxExecCmd())
	cmd.AddCommand(cliSandboxShellCmd())
	cmd.AddCommand(cliSandboxSSHCmd())
	cmd.AddCommand(cliSandboxSSHProxyCmd())
	cmd.AddCommand(cliSandboxCPCmd())
	cmd.AddCommand(cliSandboxLogsCmd())
	cmd.AddCommand(cliSandboxStatsCmd())
	cmd.AddCommand(cliSandboxTemplateCmd())
	cmd.AddCommand(cliSandboxCloneCmd())
	cmd.AddCommand(cliSandboxPublishCmd())
	cmd.AddCommand(cliSandboxUnpublishCmd())
	return cmd
}

func cliSandboxListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sandboxes in the current organization",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out []map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/sandboxes", nil, &out); err != nil {
				return fmt.Errorf("list sandboxes: %w", err)
			}
			if !remoteJSON {
				for _, row := range out {
					fmt.Printf("%s  %-20s  %-8s  %s\n", jsonFieldString(row, "id"), jsonFieldString(row, "name"), jsonFieldString(row, "observed_state"), jsonFieldString(row, "runtime_url"))
				}
				return nil
			}
			return printRemote(out)
		},
	}
}

func cliSandboxGetCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get one sandbox by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/sandboxes/"+id, nil, &out); err != nil {
				return fmt.Errorf("fetch sandbox: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxCreateCmd() *cobra.Command {
	var (
		name, hostGroup, imageRef, templateID, gitRepo, gitRef, expiresAt string
		vcpu, memoryMB, diskGB                                            int32
		autoSuspend                                                       int64
		port                                                              int32
		startStopped                                                      bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a sandbox (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			body := map[string]any{
				"name":                 strings.TrimSpace(name),
				"host_group":           strings.TrimSpace(hostGroup),
				"base_image_ref":       strings.TrimSpace(imageRef),
				"template_id":          strings.TrimSpace(templateID),
				"git_repo":             strings.TrimSpace(gitRepo),
				"git_ref":              strings.TrimSpace(gitRef),
				"vcpu":                 vcpu,
				"memory_mb":            memoryMB,
				"disk_gb":              diskGB,
				"auto_suspend_seconds": autoSuspend,
			}
			if strings.TrimSpace(expiresAt) != "" {
				body["expires_at"] = strings.TrimSpace(expiresAt)
			}
			if port > 0 {
				body["published_http_port"] = port
			}
			if startStopped {
				body["desired_state"] = "stopped"
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes", body, &out); err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Sandbox name")
	cmd.Flags().StringVar(&hostGroup, "host-group", "", "Host group (linux-sandbox or mac-sandbox)")
	cmd.Flags().StringVar(&imageRef, "image", "", "Base OCI image reference")
	cmd.Flags().StringVar(&templateID, "template", "", "Sandbox template UUID")
	cmd.Flags().StringVar(&gitRepo, "git-repo", "", "Optional repo URL")
	cmd.Flags().StringVar(&gitRef, "git-ref", "", "Optional repo ref/branch")
	cmd.Flags().Int32Var(&vcpu, "vcpu", 2, "vCPU count")
	cmd.Flags().Int32Var(&memoryMB, "memory-mb", 2048, "Memory in MB")
	cmd.Flags().Int32Var(&diskGB, "disk-gb", 10, "Disk size in GB")
	cmd.Flags().Int64Var(&autoSuspend, "auto-suspend-seconds", 0, "Idle auto-suspend timeout in seconds (0 disables auto-suspend)")
	cmd.Flags().Int32Var(&port, "port", 0, "Published HTTP port")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "Optional RFC3339 expiry time")
	cmd.Flags().BoolVar(&startStopped, "stopped", false, "Create the sandbox in stopped state")
	return cmd
}

func cliSandboxUpdateCmd() *cobra.Command {
	var sandboxID string
	var autoSuspend int64
	var imageRef string
	var vcpu, memoryMB, diskGB int32
	var expiresAt string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update sandbox settings (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			if autoSuspend < 0 {
				autoSuspend = -1
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			body := map[string]any{}
			if cmd.Flags().Changed("auto-suspend-seconds") {
				if autoSuspend < 0 {
					return fmt.Errorf("--auto-suspend-seconds must be >= 0")
				}
				body["auto_suspend_seconds"] = autoSuspend
			}
			if cmd.Flags().Changed("image") {
				body["base_image_ref"] = strings.TrimSpace(imageRef)
			}
			if cmd.Flags().Changed("vcpu") {
				body["vcpu"] = vcpu
			}
			if cmd.Flags().Changed("memory-mb") {
				body["memory_mb"] = memoryMB
			}
			if cmd.Flags().Changed("disk-gb") {
				body["disk_gb"] = diskGB
			}
			if cmd.Flags().Changed("expires-at") {
				body["expires_at"] = strings.TrimSpace(expiresAt)
			}
			if len(body) == 0 {
				return fmt.Errorf("no settings provided")
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPatch, "/api/sandboxes/"+id, body, &out); err != nil {
				return fmt.Errorf("update sandbox: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().Int64Var(&autoSuspend, "auto-suspend-seconds", -1, "Idle auto-suspend timeout in seconds (0 disables auto-suspend)")
	cmd.Flags().StringVar(&imageRef, "image", "", "Base OCI image reference")
	cmd.Flags().Int32Var(&vcpu, "vcpu", 0, "vCPU count")
	cmd.Flags().Int32Var(&memoryMB, "memory-mb", 0, "Memory in MB")
	cmd.Flags().Int32Var(&diskGB, "disk-gb", 0, "Disk size in GB")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "Optional RFC3339 expiry time")
	return cmd
}

func cliSandboxDeleteCmd() *cobra.Command {
	var sandboxID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a sandbox (org admin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			if !yes {
				return fmt.Errorf("refusing without --yes\n  kindling sandbox delete --sandbox %s --yes", id)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			resp, err := c.Do(cmd.Context(), http.MethodDelete, "/api/sandboxes/"+id, nil)
			if err != nil {
				return fmt.Errorf("delete sandbox request: %w", err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("delete failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
			}
			printRemoteMessage("sandbox deleted: " + id)
			return nil
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm destructive delete")
	return cmd
}

func cliSandboxStartCmd() *cobra.Command {
	return cliSandboxActionCmd("start", "Start or resume a sandbox")
}

func cliSandboxStopCmd() *cobra.Command {
	return cliSandboxActionCmd("stop", "Stop a sandbox into retained suspended state")
}

func cliSandboxSuspendCmd() *cobra.Command {
	return cliSandboxActionCmd("suspend", "Suspend a sandbox into retained state")
}

func cliSandboxResumeCmd() *cobra.Command {
	return cliSandboxActionCmd("resume", "Resume a suspended sandbox")
}

func cliSandboxActionCmd(action, short string) *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   action,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/"+action, map[string]any{}, &out); err != nil {
				return fmt.Errorf("%s sandbox: %w", action, err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxTemplateCmd() *cobra.Command {
	var sandboxID, name string
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Capture a sandbox template from a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/template", map[string]any{"name": strings.TrimSpace(name)}, &out); err != nil {
				return fmt.Errorf("create sandbox template: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().StringVar(&name, "name", "", "Template name")
	return cmd
}

func cliSandboxCloneCmd() *cobra.Command {
	var templateID, name string
	cmd := &cobra.Command{
		Use:   "clone",
		Short: "Clone a sandbox template into a new sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(templateID)
			if id == "" {
				return fmt.Errorf("--template is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid template id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandbox-templates/"+id+"/clone", map[string]any{"name": strings.TrimSpace(name)}, &out); err != nil {
				return fmt.Errorf("clone sandbox template: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&templateID, "template", "", "Sandbox template UUID")
	cmd.Flags().StringVar(&name, "name", "", "New sandbox name")
	return cmd
}

func cliSandboxPublishCmd() *cobra.Command {
	var sandboxID, hostname string
	var targetPort int32
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish a sandbox HTTP port",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if targetPort <= 0 {
				return fmt.Errorf("--port is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/publish-http", map[string]any{
				"target_port": targetPort,
				"hostname":    strings.TrimSpace(hostname),
			}, &out); err != nil {
				return fmt.Errorf("publish sandbox: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().Int32Var(&targetPort, "port", 0, "Target HTTP port inside the sandbox")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Optional public hostname metadata")
	return cmd
}

func cliSandboxUnpublishCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "unpublish",
		Short: "Remove a sandbox HTTP publication",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/unpublish-http", map[string]any{}, &out); err != nil {
				return fmt.Errorf("unpublish sandbox: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxExecCmd() *cobra.Command {
	var sandboxID, cwd string
	var env []string
	cmd := &cobra.Command{
		Use:   "exec --sandbox <uuid> -- <command> [args...]",
		Short: "Execute a command inside a running sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("command is required after --")
			}
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out sandboxExecResponse
			if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/exec", map[string]any{
				"argv": args,
				"cwd":  strings.TrimSpace(cwd),
				"env":  env,
			}, &out); err != nil {
				return fmt.Errorf("exec sandbox: %w", err)
			}
			if remoteJSON {
				if err := printRemote(out); err != nil {
					return err
				}
			} else if out.Output != "" {
				fmt.Fprint(os.Stdout, out.Output)
			}
			if out.ExitCode != 0 {
				return fmt.Errorf("sandbox command exited with code %d", out.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Optional working directory inside the sandbox")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Environment variable (KEY=value), repeatable")
	return cmd
}

func cliSandboxShellCmd() *cobra.Command {
	var sandboxID, cwd, shellPath string
	var env []string
	cmd := &cobra.Command{
		Use:   "shell --sandbox <uuid>",
		Short: "Open an interactive shell inside a running sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSandboxShell(cmd, sandboxID, cwd, shellPath, env)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Optional working directory inside the sandbox")
	cmd.Flags().StringVar(&shellPath, "shell", "/bin/sh", "Shell binary inside the sandbox")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Environment variable (KEY=value), repeatable")
	return cmd
}

func cliSandboxSSHCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "ssh --sandbox <uuid> [-- <ssh args...>]",
		Short: "Open an SSH session to a running sandbox using the local ssh client with managed host-key verification",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			sandbox, err := fetchSandboxSummary(cmd.Context(), c, id)
			if err != nil {
				return fmt.Errorf("fetch sandbox: %w", err)
			}
			knownHostsPath, err := writeSandboxKnownHosts(id, sandbox.SSHHostPublicKey)
			if err != nil {
				return fmt.Errorf("prepare sandbox ssh trust: %w", err)
			}
			defer os.Remove(knownHostsPath)
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate current binary: %w", err)
			}
			proxyParts := []string{shellEscape(exe), "sandbox", "ssh-proxy", "--sandbox", shellEscape(id)}
			if strings.TrimSpace(remoteProfile) != "" {
				proxyParts = append(proxyParts, "--profile", shellEscape(strings.TrimSpace(remoteProfile)))
			}
			if strings.TrimSpace(remoteAPIURL) != "" {
				proxyParts = append(proxyParts, "--api-url", shellEscape(strings.TrimSpace(remoteAPIURL)))
			}
			if strings.TrimSpace(remoteAPIKey) != "" {
				proxyParts = append(proxyParts, "--api-key", shellEscape(strings.TrimSpace(remoteAPIKey)))
			}
			sshArgs := []string{
				"-o", "ProxyCommand=" + strings.Join(proxyParts, " "),
				"-o", "StrictHostKeyChecking=yes",
				"-o", "GlobalKnownHostsFile=/dev/null",
				"-o", "UserKnownHostsFile=" + knownHostsPath,
			}
			sshArgs = append(sshArgs, args...)
			sshArgs = append(sshArgs, "kindling@"+sandboxSSHHostAlias(id))
			sshCmd := exec.CommandContext(cmd.Context(), "ssh", sshArgs...)
			sshCmd.Stdin = os.Stdin
			sshCmd.Stdout = os.Stdout
			sshCmd.Stderr = os.Stderr
			return sshCmd.Run()
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxSSHProxyCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:    "ssh-proxy --sandbox <uuid>",
		Short:  "Internal helper used by `kindling sandbox ssh`",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			ws, _, err := sandboxDialWebsocket(cmd.Context(), c, "/api/sandboxes/"+id+"/ssh/ws")
			if err != nil {
				return err
			}
			defer ws.Close()

			done := make(chan error, 2)
			go func() {
				buf := make([]byte, 32*1024)
				for {
					n, err := os.Stdin.Read(buf)
					if n > 0 {
						if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
							done <- werr
							return
						}
					}
					if err != nil {
						done <- err
						return
					}
				}
			}()
			go func() {
				for {
					_, payload, err := ws.ReadMessage()
					if err != nil {
						done <- err
						return
					}
					if len(payload) > 0 {
						if _, err := os.Stdout.Write(payload); err != nil {
							done <- err
							return
						}
					}
				}
			}()
			err = <-done
			if errors.Is(err, io.EOF) || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxCPCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "cp --sandbox <uuid> <src> <dst>",
		Short: "Copy a file into or out of a sandbox",
		Long:  "Use a leading ':' to mark the sandbox path, for example ./local.txt :/workspace/local.txt or :/workspace/log.txt ./log.txt.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			src, dst := args[0], args[1]
			srcRemote := strings.HasPrefix(src, ":")
			dstRemote := strings.HasPrefix(dst, ":")
			if srcRemote == dstRemote {
				return fmt.Errorf("exactly one side must be a sandbox path prefixed with ':'")
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			if dstRemote {
				return sandboxCopyIn(cmd, c, id, src, strings.TrimPrefix(dst, ":"))
			}
			return sandboxCopyOut(cmd, c, id, strings.TrimPrefix(src, ":"), dst)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxLogsCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Fetch recent sandbox logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out []string
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/sandboxes/"+id+"/logs", nil, &out); err != nil {
				return fmt.Errorf("sandbox logs: %w", err)
			}
			if remoteJSON {
				return printRemote(out)
			}
			for _, line := range out {
				fmt.Println(line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

func cliSandboxStatsCmd() *cobra.Command {
	var sandboxID string
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Fetch one resource stats sample for a running sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(sandboxID)
			if id == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("invalid sandbox id: %w", err)
			}
			c, err := mustRemoteClient()
			if err != nil {
				return fmt.Errorf("create client: %w", err)
			}
			var out map[string]any
			if err := c.DoJSON(cmd.Context(), http.MethodGet, "/api/sandboxes/"+id+"/stats", nil, &out); err != nil {
				return fmt.Errorf("sandbox stats: %w", err)
			}
			return printRemote(out)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	return cmd
}

type sandboxExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

type sandboxSummary struct {
	ID               string `json:"id"`
	SSHHostPublicKey string `json:"ssh_host_public_key"`
}

type sandboxUpgradedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *sandboxUpgradedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func fetchSandboxSummary(ctx context.Context, c *kcli.Client, sandboxID string) (sandboxSummary, error) {
	var out sandboxSummary
	if err := c.DoJSON(ctx, http.MethodGet, "/api/sandboxes/"+sandboxID, nil, &out); err != nil {
		return sandboxSummary{}, err
	}
	return out, nil
}

func sandboxSSHHostAlias(sandboxID string) string {
	return "sandbox-" + sandboxID[:8]
}

func writeSandboxKnownHosts(sandboxID, publicKey string) (string, error) {
	line, err := sshtrust.KnownHostsLine(sandboxSSHHostAlias(sandboxID), publicKey)
	if err != nil {
		if strings.TrimSpace(publicKey) == "" {
			return "", fmt.Errorf("sandbox SSH host key is not ready yet; ensure the sandbox is running and the guest image includes sshd and ssh-keygen")
		}
		return "", err
	}
	f, err := os.CreateTemp("", "kindling-sandbox-known-hosts-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func runSandboxExec(cmd *cobra.Command, sandboxID, cwd string, env []string, argv []string) error {
	id := strings.TrimSpace(sandboxID)
	if id == "" {
		return fmt.Errorf("--sandbox is required")
	}
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid sandbox id: %w", err)
	}
	c, err := mustRemoteClient()
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	var out sandboxExecResponse
	if err := c.DoJSON(cmd.Context(), http.MethodPost, "/api/sandboxes/"+id+"/exec", map[string]any{
		"argv": argv,
		"cwd":  strings.TrimSpace(cwd),
		"env":  env,
	}, &out); err != nil {
		return fmt.Errorf("exec sandbox: %w", err)
	}
	if remoteJSON {
		if err := printRemote(out); err != nil {
			return err
		}
	} else if out.Output != "" {
		fmt.Fprint(os.Stdout, out.Output)
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("sandbox command exited with code %d", out.ExitCode)
	}
	return nil
}

func runSandboxShell(cmd *cobra.Command, sandboxID, cwd, shellPath string, env []string) error {
	id := strings.TrimSpace(sandboxID)
	if id == "" {
		return fmt.Errorf("--sandbox is required")
	}
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid sandbox id: %w", err)
	}
	c, err := mustRemoteClient()
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	reqPath := "/api/sandboxes/" + id + "/shell/ws?shell=" + url.QueryEscape(strings.TrimSpace(shellPath))
	if strings.TrimSpace(cwd) != "" {
		reqPath += "&cwd=" + url.QueryEscape(strings.TrimSpace(cwd))
	}
	for _, item := range env {
		reqPath += "&env=" + url.QueryEscape(strings.TrimSpace(item))
	}
	ws, _, err := sandboxDialWebsocket(cmd.Context(), c, reqPath)
	if err != nil {
		return fmt.Errorf("open sandbox shell: %w", err)
	}
	defer ws.Close()

	var oldState *term.State
	stdinFD := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFD) {
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			oldState = state
			defer term.Restore(int(os.Stdin.Fd()), state)
		}
	}

	var writeMu sync.Mutex
	sendFrame := func(frame shellwire.Frame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteJSON(frame)
	}
	if term.IsTerminal(stdinFD) {
		if width, height, err := term.GetSize(stdinFD); err == nil {
			_ = sendFrame(shellwire.Frame{Type: "resize", Width: width, Height: height})
		}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if !term.IsTerminal(stdinFD) {
				continue
			}
			width, height, err := term.GetSize(stdinFD)
			if err != nil {
				continue
			}
			_ = sendFrame(shellwire.Frame{Type: "resize", Width: width, Height: height})
		}
	}()

	copyErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := sendFrame(shellwire.Frame{Type: "stdin", Data: string(buf[:n])}); werr != nil {
					copyErr <- werr
					return
				}
			}
			if err != nil {
				copyErr <- err
				return
			}
		}
	}()

	var exitCode int
	var sawExit bool
	var outErr error
	for {
		var frame shellwire.Frame
		if err := ws.ReadJSON(&frame); err != nil {
			outErr = err
			break
		}
		switch frame.Type {
		case "stdout", "stderr":
			if frame.Data != "" {
				if _, err := io.WriteString(os.Stdout, frame.Data); err != nil {
					outErr = err
					break
				}
			}
		case "exit":
			sawExit = true
			if frame.ExitCode != nil {
				exitCode = *frame.ExitCode
			}
			outErr = nil
			break
		case "error":
			if frame.Error != "" {
				_, _ = fmt.Fprintln(os.Stderr, frame.Error)
			}
		}
		if sawExit {
			break
		}
	}
	if oldState != nil {
		fmt.Fprintln(os.Stdout)
	}
	_ = ws.Close()
	if outErr != nil && !errors.Is(outErr, net.ErrClosed) && !errors.Is(outErr, io.EOF) {
		return outErr
	}
	if sawExit && exitCode != 0 {
		return fmt.Errorf("sandbox shell exited with code %d", exitCode)
	}
	select {
	case inErr := <-copyErr:
		if inErr != nil && !errors.Is(inErr, net.ErrClosed) && !errors.Is(inErr, io.EOF) {
			return inErr
		}
	default:
	}
	return nil
}

func sandboxCopyIn(cmd *cobra.Command, c *kcli.Client, sandboxID, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read local file: %w", err)
	}
	if strings.TrimSpace(remotePath) == "" || !strings.HasPrefix(strings.TrimSpace(remotePath), "/") {
		return fmt.Errorf("sandbox destination path must be absolute")
	}
	reqPath := "/api/sandboxes/" + sandboxID + "/copy-in?path=" + url.QueryEscape(strings.TrimSpace(remotePath))
	resp, err := sandboxRawRequest(cmd, c, http.MethodPost, reqPath, bytes.NewReader(data), "application/octet-stream")
	if err != nil {
		return fmt.Errorf("copy into sandbox: %w", err)
	}
	defer resp.Body.Close()
	if err := sandboxResponseError(resp, "copy into sandbox"); err != nil {
		return err
	}
	printRemoteMessage("copied into sandbox: " + filepath.Base(localPath) + " -> " + strings.TrimSpace(remotePath))
	return nil
}

func sandboxCopyOut(cmd *cobra.Command, c *kcli.Client, sandboxID, remotePath, localPath string) error {
	if strings.TrimSpace(remotePath) == "" || !strings.HasPrefix(strings.TrimSpace(remotePath), "/") {
		return fmt.Errorf("sandbox source path must be absolute")
	}
	reqPath := "/api/sandboxes/" + sandboxID + "/copy-out?path=" + url.QueryEscape(strings.TrimSpace(remotePath))
	resp, err := sandboxRawRequest(cmd, c, http.MethodGet, reqPath, nil, "")
	if err != nil {
		return fmt.Errorf("copy out of sandbox: %w", err)
	}
	defer resp.Body.Close()
	if err := sandboxResponseError(resp, "copy out of sandbox"); err != nil {
		return err
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read sandbox response: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local directory: %w", err)
	}
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return fmt.Errorf("write local file: %w", err)
	}
	printRemoteMessage("copied out of sandbox: " + strings.TrimSpace(remotePath) + " -> " + localPath)
	return nil
}

func sandboxRawRequest(cmd *cobra.Command, c *kcli.Client, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(c.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	} else if strings.TrimSpace(c.SessionToken) != "" {
		req.Header.Set("Cookie", auth.SessionCookieName+"="+strings.TrimSpace(c.SessionToken))
	}
	return c.HTTP.Do(req)
}

func sandboxUpgradeRequest(ctx context.Context, c *kcli.Client, method, path, upgrade string) (io.ReadWriteCloser, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	baseURL, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	host := baseURL.Host
	if !strings.Contains(host, ":") {
		if baseURL.Scheme == "https" {
			host = net.JoinHostPort(host, "443")
		} else {
			host = net.JoinHostPort(host, "80")
		}
	}
	dialer := &net.Dialer{}
	var conn net.Conn
	switch baseURL.Scheme {
	case "https":
		conn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: baseURL.Hostname()})
	default:
		conn, err = dialer.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}

	targetURL := *baseURL
	targetURL.Path = path
	targetURL.RawQuery = ""
	if strings.Contains(path, "?") {
		rawPath, rawQuery, _ := strings.Cut(path, "?")
		targetURL.Path = rawPath
		targetURL.RawQuery = rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL.String(), nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", upgrade)
	if strings.TrimSpace(c.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	} else if strings.TrimSpace(c.SessionToken) != "" {
		req.Header.Set("Cookie", auth.SessionCookieName+"="+strings.TrimSpace(c.SessionToken))
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		_ = conn.Close()
		return nil, fmt.Errorf("upgrade failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &sandboxUpgradedConn{Conn: conn, reader: reader}, nil
}

func sandboxResponseError(resp *http.Response, action string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	var apiErr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &apiErr) == nil && strings.TrimSpace(apiErr.Error) != "" {
		return fmt.Errorf("%s: API %d (%s): %s", action, resp.StatusCode, strings.TrimSpace(apiErr.Code), strings.TrimSpace(apiErr.Error))
	}
	return fmt.Errorf("%s failed (%d): %s", action, resp.StatusCode, strings.TrimSpace(string(body)))
}

func sandboxDialWebsocket(ctx context.Context, c *kcli.Client, path string) (*websocket.Conn, *http.Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	baseURL, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, nil, err
	}
	scheme := "ws"
	if strings.EqualFold(baseURL.Scheme, "https") {
		scheme = "wss"
	}
	targetURL := &url.URL{
		Scheme: scheme,
		Host:   baseURL.Host,
		Path:   path,
	}
	if strings.Contains(path, "?") {
		rawPath, rawQuery, _ := strings.Cut(path, "?")
		targetURL.Path = rawPath
		targetURL.RawQuery = rawQuery
	}
	header := make(http.Header)
	if strings.TrimSpace(c.APIKey) != "" {
		header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	} else if strings.TrimSpace(c.SessionToken) != "" {
		header.Set("Cookie", auth.SessionCookieName+"="+strings.TrimSpace(c.SessionToken))
	}
	return websocket.DefaultDialer.DialContext(ctx, targetURL.String(), header)
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!#&*()[]{}<>?|;") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
