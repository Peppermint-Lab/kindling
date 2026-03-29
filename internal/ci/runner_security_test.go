package ci

import (
	"strings"
	"testing"
)

// fakeHostEnv builds a slice of KEY=VALUE strings simulating the host
// process environment. It includes both allowlisted and sensitive vars.
func fakeHostEnv() []string {
	return []string{
		// Allowlisted host variables.
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/home/ci",
		"LANG=en_US.UTF-8",
		"USER=ci",
		"SHELL=/bin/bash",
		"TERM=xterm-256color",
		"TMPDIR=/tmp",
		// Sensitive secrets that must NOT leak.
		"DATABASE_URL=postgres://secret@localhost/db",
		"KINDLING_MASTER_KEY=supersecretkey123",
		"KINDLING_MASTER_KEY_PATH=/etc/kindling/master.key",
		"AWS_SECRET_ACCESS_KEY=AKIASECRET123",
		"GITHUB_TOKEN=ghp_token123",
		"SLACK_TOKEN=xoxb-slack-token",
		"MY_CUSTOM_TOKEN=custom_token_value",
		"DEPLOY_TOKEN=deploy-secret",
		"NPM_TOKEN=npm-secret-token",
		"DOCKER_TOKEN=docker-secret",
		"CI_TOKEN=ci-secret-token",
		// Arbitrary host variables that should NOT leak.
		"CI=true",
		"PWD=/some/dir",
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"CUSTOM_DEBUG_FLAG=verbose",
		"EDITOR=vim",
		"GOPATH=/Users/test/go",
		"HOSTNAME=build-server",
		"SHLVL=2",
		"SOME_RANDOM_VAR=random_value",
	}
}

// expectedAllowlistKeys are the host variables that buildBaseEnvFrom
// should pass through.
var expectedAllowlistKeys = []string{
	"PATH", "HOME", "LANG", "USER", "SHELL", "TERM", "TMPDIR",
}

// TestBuildBaseEnv_AllowlistOnly verifies that buildBaseEnvFrom returns
// only allowlisted host variables plus explicit overrides.
// Fulfills VAL-CIENV-001.
func TestBuildBaseEnv_AllowlistOnly(t *testing.T) {
	t.Parallel()

	result := buildBaseEnvFrom(fakeHostEnv(), nil)

	// Verify allowlisted host variables are present.
	for _, key := range expectedAllowlistKeys {
		if _, ok := result[key]; !ok {
			t.Errorf("expected allowlisted variable %s to be present", key)
		}
	}

	// Verify sensitive variables are NOT present.
	sensitiveKeys := []string{
		"DATABASE_URL", "KINDLING_MASTER_KEY", "KINDLING_MASTER_KEY_PATH",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "SLACK_TOKEN",
		"MY_CUSTOM_TOKEN", "DEPLOY_TOKEN", "NPM_TOKEN",
		"DOCKER_TOKEN", "CI_TOKEN",
	}
	for _, key := range sensitiveKeys {
		if val, ok := result[key]; ok {
			t.Errorf("sensitive variable %s should not be in CI env, got %q", key, val)
		}
	}
}

// TestBuildBaseEnv_WorkflowVarsSurvive verifies that workflow/job/step
// overrides are correctly merged into the environment.
// Fulfills VAL-CIENV-002.
func TestBuildBaseEnv_WorkflowVarsSurvive(t *testing.T) {
	t.Parallel()

	overrides := map[string]string{
		"MY_WORKFLOW_VAR": "workflow_value",
		"NODE_ENV":        "production",
		"CI":              "true",
		"DEPLOY_TARGET":   "staging",
	}

	result := buildBaseEnvFrom(fakeHostEnv(), overrides)

	for key, expected := range overrides {
		got, ok := result[key]
		if !ok {
			t.Errorf("workflow override %s not present in result", key)
			continue
		}
		if got != expected {
			t.Errorf("workflow override %s = %q, want %q", key, got, expected)
		}
	}

	// Allowlisted host vars should also be present alongside overrides.
	for _, key := range expectedAllowlistKeys {
		if _, ok := result[key]; !ok {
			t.Errorf("expected allowlisted variable %s to be present alongside overrides", key)
		}
	}
}

// TestBuildBaseEnv_SensitiveSecretsExcluded verifies that sensitive
// host secrets never reach CI jobs unless the workflow explicitly defines them.
// Fulfills VAL-CIENV-003.
func TestBuildBaseEnv_SensitiveSecretsExcluded(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"DATABASE_URL", "KINDLING_MASTER_KEY", "KINDLING_MASTER_KEY_PATH",
		"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "DEPLOY_TOKEN",
		"NPM_TOKEN", "DOCKER_TOKEN", "CI_TOKEN",
	}

	// No overrides — secrets should not appear from host alone.
	result := buildBaseEnvFrom(fakeHostEnv(), nil)

	for _, key := range secrets {
		if val, ok := result[key]; ok {
			t.Errorf("secret %s leaked into CI env with value %q", key, val)
		}
	}

	// Verify that an explicit workflow override for a "secret" key DOES survive.
	result2 := buildBaseEnvFrom(fakeHostEnv(), map[string]string{
		"GITHUB_TOKEN": "explicit-workflow-token",
	})
	if got := result2["GITHUB_TOKEN"]; got != "explicit-workflow-token" {
		t.Errorf("explicit GITHUB_TOKEN override = %q, want %q", got, "explicit-workflow-token")
	}
}

// TestBuildBaseEnv_ClosedByDefault verifies the allowlist is closed —
// arbitrary new host variables do not leak through.
// Fulfills VAL-CIENV-004.
func TestBuildBaseEnv_ClosedByDefault(t *testing.T) {
	t.Parallel()

	result := buildBaseEnvFrom(fakeHostEnv(), nil)

	// Build the allowlist set for checking.
	allowlistSet := make(map[string]bool, len(expectedAllowlistKeys))
	for _, key := range expectedAllowlistKeys {
		allowlistSet[key] = true
	}

	for key := range result {
		if !allowlistSet[key] {
			t.Errorf("unexpected host variable %s leaked into CI env (value=%q)", key, result[key])
		}
	}
}

// TestBuildBaseEnv_ExplicitOverridesWin verifies that explicit overrides
// take precedence over host allowlist values.
func TestBuildBaseEnv_ExplicitOverridesWin(t *testing.T) {
	t.Parallel()

	overrides := map[string]string{
		"PATH": "/custom/override/path",
	}

	result := buildBaseEnvFrom(fakeHostEnv(), overrides)

	if got := result["PATH"]; got != "/custom/override/path" {
		t.Errorf("PATH = %q, want %q (override should win)", got, "/custom/override/path")
	}
}

// TestBuildBaseEnv_RunnerInjectedVarsNotFromHost verifies that runner-injected
// operational variables (GITHUB_WORKSPACE, GITHUB_OUTPUT) don't come from
// the host — they are appended by runCommandStep.
func TestBuildBaseEnv_RunnerInjectedVarsNotFromHost(t *testing.T) {
	t.Parallel()

	// Simulate host env that includes GITHUB_WORKSPACE.
	hostPairs := append(fakeHostEnv(),
		"GITHUB_WORKSPACE=/host/workspace",
		"GITHUB_OUTPUT=/host/output",
	)

	result := buildBaseEnvFrom(hostPairs, nil)

	// Runner-injected vars should NOT come from host.
	if _, ok := result["GITHUB_WORKSPACE"]; ok {
		t.Error("GITHUB_WORKSPACE should not be in base env from host")
	}
	if _, ok := result["GITHUB_OUTPUT"]; ok {
		t.Error("GITHUB_OUTPUT should not be in base env from host")
	}
}

// TestBuildProcessEnv_WorkflowAndJobVars verifies that buildProcessEnv
// correctly layers job env vars on top of the sanitized base.
func TestBuildProcessEnv_WorkflowAndJobVars(t *testing.T) {
	t.Parallel()

	base := buildBaseEnvFrom(fakeHostEnv(), nil)

	jobEnv := map[string]string{
		"NODE_ENV": "production",
		"CI":       "true",
	}

	ev := evalContext{}
	result := buildProcessEnv(base, jobEnv, nil, ev)

	// Job vars should be present.
	if got := result["NODE_ENV"]; got != "production" {
		t.Errorf("NODE_ENV = %q, want %q", got, "production")
	}
	if got := result["CI"]; got != "true" {
		t.Errorf("CI = %q, want %q", got, "true")
	}

	// Allowlisted host var should survive.
	if got := result["PATH"]; got != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want %q", got, "/usr/local/bin:/usr/bin:/bin")
	}

	// Sensitive host var should NOT be present.
	if _, ok := result["DATABASE_URL"]; ok {
		t.Error("DATABASE_URL should not be in process env")
	}
}

// TestMapToEnv_NoSensitiveVarLeakage verifies the full pipeline from
// buildBaseEnvFrom → mapToEnv produces clean env slices.
func TestMapToEnv_NoSensitiveVarLeakage(t *testing.T) {
	t.Parallel()

	base := buildBaseEnvFrom(fakeHostEnv(), map[string]string{
		"MY_VAR": "my_value",
	})

	envSlice := mapToEnv(base)

	for _, entry := range envSlice {
		key := entry[:strings.IndexByte(entry, '=')]
		if key == "DATABASE_URL" || key == "AWS_SECRET_ACCESS_KEY" {
			t.Errorf("sensitive variable %s found in env slice", key)
		}
	}

	// Verify allowlisted and override vars ARE present.
	found := map[string]bool{}
	for _, entry := range envSlice {
		key := entry[:strings.IndexByte(entry, '=')]
		found[key] = true
	}
	if !found["PATH"] {
		t.Error("PATH not found in env slice")
	}
	if !found["MY_VAR"] {
		t.Error("MY_VAR not found in env slice")
	}
}

// TestBuildBaseEnv_EmptyHostEnv verifies the function works with an
// empty host environment.
func TestBuildBaseEnv_EmptyHostEnv(t *testing.T) {
	t.Parallel()

	result := buildBaseEnvFrom(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil host env and nil overrides, got %d entries", len(result))
	}

	result2 := buildBaseEnvFrom(nil, map[string]string{"CI": "true"})
	if got := result2["CI"]; got != "true" {
		t.Errorf("CI = %q, want %q", got, "true")
	}
	if len(result2) != 1 {
		t.Errorf("expected 1 entry for nil host env with one override, got %d", len(result2))
	}
}

// TestBuildBaseEnv_MalformedHostEntries verifies the function handles
// malformed KEY=VALUE entries gracefully.
func TestBuildBaseEnv_MalformedHostEntries(t *testing.T) {
	t.Parallel()

	hostPairs := []string{
		"PATH=/usr/bin",
		"",          // empty entry
		"=value",    // empty key
		"NOEQUALS",  // no equals sign
		"HOME=/home/ci",
	}

	result := buildBaseEnvFrom(hostPairs, nil)

	if got := result["PATH"]; got != "/usr/bin" {
		t.Errorf("PATH = %q, want %q", got, "/usr/bin")
	}
	if got := result["HOME"]; got != "/home/ci" {
		t.Errorf("HOME = %q, want %q", got, "/home/ci")
	}
	if len(result) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(result), result)
	}
}
