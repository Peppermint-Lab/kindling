package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/kindlingvm/kindling/internal/auth"
	kcli "github.com/kindlingvm/kindling/internal/cli"
	"github.com/spf13/cobra"
)

func cliSandboxRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes via the control-plane API",
	}
	cmd.AddCommand(cliSandboxListCmd())
	cmd.AddCommand(cliSandboxGetCmd())
	cmd.AddCommand(cliSandboxCreateCmd())
	cmd.AddCommand(cliSandboxDeleteCmd())
	cmd.AddCommand(cliSandboxStartCmd())
	cmd.AddCommand(cliSandboxStopCmd())
	cmd.AddCommand(cliSandboxSuspendCmd())
	cmd.AddCommand(cliSandboxResumeCmd())
	cmd.AddCommand(cliSandboxExecCmd())
	cmd.AddCommand(cliSandboxShellCmd())
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
	cmd.Flags().Int64Var(&autoSuspend, "auto-suspend-seconds", 900, "Idle auto-suspend timeout in seconds")
	cmd.Flags().Int32Var(&port, "port", 0, "Published HTTP port")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "Optional RFC3339 expiry time")
	cmd.Flags().BoolVar(&startStopped, "stopped", false, "Create the sandbox in stopped state")
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
		Use:   "shell --sandbox <uuid> -- <shell-snippet>",
		Short: "Run a shell snippet inside a running sandbox",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("shell snippet is required after --")
			}
			runArgs := []string{strings.TrimSpace(shellPath), "-lc", strings.Join(args, " ")}
			return runSandboxExec(cmd, sandboxID, cwd, env, runArgs)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Sandbox UUID")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Optional working directory inside the sandbox")
	cmd.Flags().StringVar(&shellPath, "shell", "/bin/sh", "Shell binary inside the sandbox")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Environment variable (KEY=value), repeatable")
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
