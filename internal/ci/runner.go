package ci

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type WorkflowRunner interface {
	Run(ctx context.Context, plan ExecutionPlan, opts RunOptions) (RunResult, error)
	Backend() string
	IsMicroVM() bool
}

type LocalWorkflowRunner struct{}

func NewLocalWorkflowRunner() *LocalWorkflowRunner {
	return &LocalWorkflowRunner{}
}

func (r *LocalWorkflowRunner) Backend() string { return "local_host" }

func (r *LocalWorkflowRunner) IsMicroVM() bool { return false }

type RunOptions struct {
	Stdout io.Writer
	Stderr io.Writer
	Env    map[string]string
}

type RunResult struct {
	Jobs         []JobRunResult
	Artifacts    []ArtifactInfo
	ArtifactRoot string
	Backend      string
}

type JobRunResult struct {
	ID      string
	Name    string
	Result  string
	Outputs map[string]string
}

type ArtifactInfo struct {
	Name string
	Path string
}

type evalContext struct {
	repoRoot string
	plan     ExecutionPlan
	env      map[string]string
	steps    map[string]stepState
	needs    map[string]jobState
}

type stepState struct {
	Outputs map[string]string
}

type jobState struct {
	Result  string
	Outputs map[string]string
}

type artifactStore struct {
	root string
	mu   sync.Mutex
	info map[string]string
}

func newArtifactStore() (*artifactStore, error) {
	root, err := os.MkdirTemp("", "kindling-ci-artifacts-*")
	if err != nil {
		return nil, err
	}
	return &artifactStore{root: root, info: map[string]string{}}, nil
}

func (s *artifactStore) Path(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.info[name]
	return p, ok
}

func (s *artifactStore) Save(name, src string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := filepath.Join(s.root, sanitizeName(name))
	_ = os.RemoveAll(dst)
	if err := copyPath(src, dst); err != nil {
		return err
	}
	s.info[name] = dst
	return nil
}

func (s *artifactStore) List() []ArtifactInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ArtifactInfo, 0, len(s.info))
	for name, path := range s.info {
		out = append(out, ArtifactInfo{Name: name, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *LocalWorkflowRunner) Run(ctx context.Context, plan ExecutionPlan, opts RunOptions) (RunResult, error) {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	artifacts, err := newArtifactStore()
	if err != nil {
		return RunResult{}, err
	}

	cache, err := newCacheStore()
	if err != nil {
		return RunResult{}, err
	}

	sshEnv := map[string]string{}
	var sshCleanup func()
	defer func() {
		if sshCleanup != nil {
			sshCleanup()
		}
	}()

	ctxState := evalContext{
		repoRoot: plan.RepoRoot,
		plan:     plan,
		env:      buildBaseEnv(opts.Env),
		steps:    map[string]stepState{},
		needs:    map[string]jobState{},
	}
	jobResults := make([]JobRunResult, 0, len(plan.Jobs))

	for _, job := range plan.Jobs {
		jobCtx := ctxState
		jobCtx.steps = map[string]stepState{}
		if shouldSkip, err := shouldSkipExpr(job.If, jobCtx); err != nil {
			return RunResult{}, fmt.Errorf("job %s if: %w", job.ID, err)
		} else if shouldSkip {
			ctxState.needs[job.ID] = jobState{Result: "skipped", Outputs: map[string]string{}}
			jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: "skipped", Outputs: map[string]string{}})
			fmt.Fprintf(stdout, "==> Job %s skipped\n", job.ID)
			continue
		}

		fmt.Fprintf(stdout, "==> Job %s (%s)\n", job.ID, job.Name)
		jobEnv := buildProcessEnv(ctxState.env, job.Env, sshEnv, jobCtx)
		jobResult := "success"
		for _, step := range job.Steps {
			stepCtx := jobCtx
			if skip, err := shouldSkipExpr(step.If, stepCtx); err != nil {
				return RunResult{}, fmt.Errorf("job %s step %s if: %w", job.ID, step.Name, err)
			} else if skip {
				fmt.Fprintf(stdout, "----> Step %s skipped\n", step.Name)
				continue
			}
			fmt.Fprintf(stdout, "----> Step %s\n", step.Name)
			stepEnv := cloneMap(jobEnv)
			mergeEnv(stepEnv, step.Env)
			if err := interpolateEnv(stepEnv, stepCtx); err != nil {
				return RunResult{}, fmt.Errorf("job %s step %s env: %w", job.ID, step.Name, err)
			}
			outputs, cleanup, err := r.runStep(ctx, plan.RepoRoot, step, stepEnv, stdout, stderr, stepCtx, artifacts, cache)
			if cleanup != nil {
				defer cleanup()
			}
			if err != nil {
				jobResult = "failure"
				ctxState.needs[job.ID] = jobState{Result: jobResult, Outputs: map[string]string{}}
				jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: jobResult, Outputs: map[string]string{}})
				return RunResult{Jobs: jobResults, Artifacts: artifacts.List(), ArtifactRoot: artifacts.root, Backend: r.Backend()}, fmt.Errorf("job %s step %s: %w", job.ID, step.Name, err)
			}
			if step.Kind == StepKindSSHAgent {
				sshEnv = cloneMap(stepEnv)
				sshCleanup = cleanup
			}
			if step.ID != "" {
				jobCtx.steps[step.ID] = stepState{Outputs: outputs}
			}
		}

		outputs, err := evaluateJobOutputs(job.Outputs, jobCtx)
		if err != nil {
			return RunResult{}, fmt.Errorf("job %s outputs: %w", job.ID, err)
		}
		ctxState.needs[job.ID] = jobState{Result: jobResult, Outputs: outputs}
		jobResults = append(jobResults, JobRunResult{ID: job.ID, Name: job.Name, Result: jobResult, Outputs: outputs})

		for _, entry := range cache.entries {
			_ = cache.save(entry.Key, filepath.Join(plan.RepoRoot, entry.Path))
		}
	}

	return RunResult{Jobs: jobResults, Artifacts: artifacts.List(), ArtifactRoot: artifacts.root, Backend: r.Backend()}, nil
}

func (r *LocalWorkflowRunner) runStep(
	ctx context.Context,
	repoRoot string,
	step CompiledStep,
	env map[string]string,
	stdout, stderr io.Writer,
	ev evalContext,
	artifacts *artifactStore,
	cache *cacheStore,
) (map[string]string, func(), error) {
	switch step.Kind {
	case StepKindCheckout:
		return map[string]string{}, nil, nil
	case StepKindSetupGo:
		if _, err := exec.LookPath("go"); err != nil {
			return nil, nil, fmt.Errorf("go not found on PATH")
		}
		return map[string]string{}, nil, nil
	case StepKindSetupNode:
		if _, err := exec.LookPath("node"); err != nil {
			return nil, nil, fmt.Errorf("node not found on PATH")
		}
		if _, err := exec.LookPath("npm"); err != nil {
			return nil, nil, fmt.Errorf("npm not found on PATH")
		}
		return map[string]string{}, nil, nil
	case StepKindUploadArtifact:
		name, err := interpolateValue(step.With["name"], ev)
		if err != nil {
			return nil, nil, err
		}
		pathValue, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(name) == "" || strings.TrimSpace(pathValue) == "" {
			return nil, nil, fmt.Errorf("upload-artifact requires name and path")
		}
		src := pathValue
		if !filepath.IsAbs(src) {
			src = filepath.Join(repoRoot, src)
		}
		if err := artifacts.Save(name, src); err != nil {
			return nil, nil, fmt.Errorf("save artifact %s: %w", name, err)
		}
		return map[string]string{}, nil, nil
	case StepKindDownloadArtifact:
		name, err := interpolateValue(step.With["name"], ev)
		if err != nil {
			return nil, nil, err
		}
		target, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(target) == "" {
			target = "."
		}
		src, ok := artifacts.Path(name)
		if !ok {
			return nil, nil, fmt.Errorf("artifact %q not found", name)
		}
		dst := target
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(repoRoot, dst)
		}
		if err := copyArtifactContents(src, dst); err != nil {
			return nil, nil, fmt.Errorf("restore artifact %s: %w", name, err)
		}
		return map[string]string{}, nil, nil
	case StepKindCache:
		pathValue, err := interpolateValue(step.With["path"], ev)
		if err != nil {
			return nil, nil, err
		}
		keyValue, err := interpolateValue(step.With["key"], ev)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(pathValue) == "" || strings.TrimSpace(keyValue) == "" {
			return nil, nil, fmt.Errorf("actions/cache requires path and key")
		}
		if err := cache.restore(keyValue, filepath.Join(repoRoot, pathValue)); err != nil {
			return nil, nil, err
		}
		cache.track(keyValue, pathValue)
		return map[string]string{}, nil, nil
	case StepKindPathsFilter:
		filters, err := interpolateValue(step.With["filters"], ev)
		if err != nil {
			return nil, nil, err
		}
		changed, err := gitChangedFiles(ctx, repoRoot)
		if err != nil {
			return nil, nil, err
		}
		outputs, err := evaluatePathsFilter(filters, changed)
		if err != nil {
			return nil, nil, err
		}
		return outputs, nil, nil
	case StepKindSSHAgent:
		key, err := interpolateValue(step.With["ssh-private-key"], ev)
		if err != nil {
			return nil, nil, err
		}
		cleanup, envOut, err := startSSHAgent(ctx, key, env, stdout, stderr)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range envOut {
			env[k] = v
		}
		return map[string]string{}, cleanup, nil
	case StepKindRun:
		return runCommandStep(ctx, repoRoot, step, env, stdout, stderr, ev)
	default:
		return nil, nil, fmt.Errorf("unsupported step kind %s", step.Kind)
	}
}

func runCommandStep(ctx context.Context, repoRoot string, step CompiledStep, env map[string]string, stdout, stderr io.Writer, ev evalContext) (map[string]string, func(), error) {
	commandText, err := interpolateValue(step.Run, ev)
	if err != nil {
		return nil, nil, err
	}
	workingDir := strings.TrimSpace(step.WorkingDirectory)
	if workingDir == "" {
		workingDir = repoRoot
	} else if !filepath.IsAbs(workingDir) {
		workingDir = filepath.Join(repoRoot, workingDir)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, nil, err
	}
	outputFile, err := os.CreateTemp("", "kindling-gh-output-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(outputFile.Name())
	defer outputFile.Close()

	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", commandText)
	cmd.Dir = workingDir
	cmd.Env = mapToEnv(env)
	cmd.Env = append(cmd.Env, "GITHUB_OUTPUT="+outputFile.Name())
	cmd.Env = append(cmd.Env, "GITHUB_WORKSPACE="+repoRoot)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, err
	}
	outputs, err := parseOutputFile(outputFile.Name())
	return outputs, nil, err
}

// hostEnvAllowlist defines the only host environment variables that are
// permitted to pass through to CI job environments. This prevents leaking
// secrets (DATABASE_URL, *_TOKEN, KINDLING_MASTER_KEY, etc.) from the host
// process into CI steps. The list is intentionally closed: any new host
// variable must be explicitly added here.
var hostEnvAllowlist = map[string]bool{
	"PATH":   true,
	"HOME":   true,
	"LANG":   true,
	"USER":   true,
	"SHELL":  true,
	"TERM":   true,
	"TMPDIR": true,
}

func buildBaseEnv(overrides map[string]string) map[string]string {
	return buildBaseEnvFrom(os.Environ(), overrides)
}

// buildBaseEnvFrom constructs a sanitized base environment for CI jobs.
// Only variables in the hostEnvAllowlist are copied from the host
// environment (hostPairs). Workflow/job/step overrides are then merged
// on top, allowing explicit declarations to set any variable (including
// those not on the allowlist).
func buildBaseEnvFrom(hostPairs []string, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(hostEnvAllowlist)+len(overrides))
	for _, kv := range hostPairs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		key := kv[:i]
		if hostEnvAllowlist[key] {
			out[key] = kv[i+1:]
		}
	}
	mergeEnv(out, overrides)
	return out
}

func buildProcessEnv(base, extra, sshEnv map[string]string, ev evalContext) map[string]string {
	out := cloneMap(base)
	mergeEnv(out, sshEnv)
	mergeEnv(out, extra)
	_ = interpolateEnv(out, ev)
	return out
}

func interpolateEnv(env map[string]string, ev evalContext) error {
	for key, value := range env {
		resolved, err := interpolateValue(value, ev)
		if err != nil {
			return err
		}
		env[key] = resolved
	}
	return nil
}

func mergeEnv(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}

func mapToEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func interpolateValue(value string, ev evalContext) (string, error) {
	return interpolateString(value, func(expr string) (string, error) {
		return evalScalar(strings.TrimSpace(expr), ev)
	})
}

func interpolateString(input string, eval func(string) (string, error)) (string, error) {
	out := input
	for {
		start := strings.Index(out, "${{")
		if start < 0 {
			return out, nil
		}
		end := strings.Index(out[start+3:], "}}")
		if end < 0 {
			return "", fmt.Errorf("unterminated expression in %q", input)
		}
		end += start + 3
		expr := strings.TrimSpace(out[start+3 : end])
		value, err := eval(expr)
		if err != nil {
			return "", err
		}
		out = out[:start] + value + out[end+2:]
	}
}

func shouldSkipExpr(expr string, ev evalContext) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, nil
	}
	ok, err := evalBool(expr, ev)
	if err != nil {
		return false, err
	}
	return !ok, nil
}

func evaluateJobOutputs(outputs map[string]string, ev evalContext) (map[string]string, error) {
	if len(outputs) == 0 {
		return map[string]string{}, nil
	}
	resolved := make(map[string]string, len(outputs))
	for key, expr := range outputs {
		value, err := interpolateValue(expr, ev)
		if err != nil {
			return nil, err
		}
		resolved[key] = value
	}
	return resolved, nil
}

func evalScalar(expr string, ev evalContext) (string, error) {
	expr = strings.TrimSpace(expr)
	switch {
	case expr == "":
		return "", nil
	case strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'"):
		return strings.Trim(expr, "'"), nil
	case strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\""):
		return strings.Trim(expr, "\""), nil
	case strings.HasPrefix(expr, "hashFiles("):
		return evalHashFiles(expr, ev)
	default:
		return lookupValue(expr, ev), nil
	}
}

func lookupValue(path string, ev evalContext) string {
	switch {
	case path == "github.workspace":
		return ev.repoRoot
	case path == "github.workflow":
		return ev.plan.WorkflowName
	case path == "github.event_name":
		return ev.plan.Event
	case strings.HasPrefix(path, "github.event.inputs."):
		return ev.plan.Inputs[strings.TrimPrefix(path, "github.event.inputs.")]
	case strings.HasPrefix(path, "steps."):
		parts := strings.Split(path, ".")
		if len(parts) == 4 && parts[2] == "outputs" {
			return ev.steps[parts[1]].Outputs[parts[3]]
		}
	case strings.HasPrefix(path, "needs."):
		parts := strings.Split(path, ".")
		if len(parts) >= 3 {
			jobID := parts[1]
			switch parts[2] {
			case "result":
				return ev.needs[jobID].Result
			case "outputs":
				if len(parts) == 4 {
					return ev.needs[jobID].Outputs[parts[3]]
				}
			}
		}
	case strings.HasPrefix(path, "secrets."):
		return os.Getenv(strings.TrimPrefix(path, "secrets."))
	case strings.HasPrefix(path, "vars."):
		return os.Getenv(strings.TrimPrefix(path, "vars."))
	}
	return ""
}

func evalHashFiles(expr string, ev evalContext) (string, error) {
	start := strings.IndexByte(expr, '(')
	end := strings.LastIndexByte(expr, ')')
	if start < 0 || end <= start {
		return "", fmt.Errorf("invalid hashFiles expression %q", expr)
	}
	arg := strings.TrimSpace(expr[start+1 : end])
	arg = strings.Trim(arg, `'"`)
	matches, err := filepath.Glob(filepath.Join(ev.repoRoot, arg))
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(match)
		if err != nil {
			return "", err
		}
		h.Write([]byte(match))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type token struct {
	kind string
	text string
}

func evalBool(expr string, ev evalContext) (bool, error) {
	tokens, err := tokenize(expr)
	if err != nil {
		return false, err
	}
	p := &boolParser{tokens: tokens, ev: ev}
	value, err := p.parseExpr()
	if err != nil {
		return false, err
	}
	if p.pos != len(tokens) {
		return false, fmt.Errorf("unexpected token %q", tokens[p.pos].text)
	}
	return value, nil
}

type boolParser struct {
	tokens []token
	pos    int
	ev     evalContext
}

func (p *boolParser) parseExpr() (bool, error) {
	return p.parseOr()
}

func (p *boolParser) parseOr() (bool, error) {
	left, err := p.parseAnd()
	if err != nil {
		return false, err
	}
	for p.match("||") {
		right, err := p.parseAnd()
		if err != nil {
			return false, err
		}
		left = left || right
	}
	return left, nil
}

func (p *boolParser) parseAnd() (bool, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return false, err
	}
	for p.match("&&") {
		right, err := p.parsePrimary()
		if err != nil {
			return false, err
		}
		left = left && right
	}
	return left, nil
}

func (p *boolParser) parsePrimary() (bool, error) {
	if p.match("(") {
		value, err := p.parseExpr()
		if err != nil {
			return false, err
		}
		if !p.match(")") {
			return false, fmt.Errorf("missing )")
		}
		return value, nil
	}
	if p.peekText("always") {
		p.pos++
		if p.match("(") {
			if !p.match(")") {
				return false, fmt.Errorf("always() missing )")
			}
		}
		return true, nil
	}
	left, err := p.parseValue()
	if err != nil {
		return false, err
	}
	if p.match("==") {
		right, err := p.parseValue()
		if err != nil {
			return false, err
		}
		return left == right, nil
	}
	if p.match("!=") {
		right, err := p.parseValue()
		if err != nil {
			return false, err
		}
		return left != right, nil
	}
	return truthy(left), nil
}

func (p *boolParser) parseValue() (string, error) {
	if p.pos >= len(p.tokens) {
		return "", fmt.Errorf("unexpected end of expression")
	}
	tok := p.tokens[p.pos]
	p.pos++
	switch tok.kind {
	case "string":
		return tok.text, nil
	case "ident":
		return evalScalar(tok.text, p.ev)
	default:
		return "", fmt.Errorf("unexpected token %q", tok.text)
	}
}

func (p *boolParser) match(text string) bool {
	if p.pos >= len(p.tokens) || p.tokens[p.pos].text != text {
		return false
	}
	p.pos++
	return true
}

func (p *boolParser) peekText(text string) bool {
	return p.pos < len(p.tokens) && p.tokens[p.pos].text == text
}

func tokenize(expr string) ([]token, error) {
	tokens := make([]token, 0, len(expr)/2)
	for i := 0; i < len(expr); {
		switch expr[i] {
		case ' ', '\t', '\n', '\r':
			i++
		case '(', ')':
			tokens = append(tokens, token{kind: "sym", text: expr[i : i+1]})
			i++
		case '&', '|', '=', '!':
			if i+1 < len(expr) {
				op := expr[i : i+2]
				if op == "&&" || op == "||" || op == "==" || op == "!=" {
					tokens = append(tokens, token{kind: "op", text: op})
					i += 2
					continue
				}
			}
			return nil, fmt.Errorf("unsupported operator near %q", expr[i:])
		case '\'', '"':
			quote := expr[i]
			j := i + 1
			for j < len(expr) && expr[j] != quote {
				j++
			}
			if j >= len(expr) {
				return nil, fmt.Errorf("unterminated string in %q", expr)
			}
			tokens = append(tokens, token{kind: "string", text: expr[i+1 : j]})
			i = j + 1
		default:
			j := i
			for j < len(expr) {
				ch := expr[j]
				if strings.ContainsRune(" \t\r\n()&|=!", rune(ch)) {
					break
				}
				j++
			}
			tokens = append(tokens, token{kind: "ident", text: strings.TrimSpace(expr[i:j])})
			i = j
		}
	}
	return tokens, nil
}

func truthy(value string) bool {
	v := strings.TrimSpace(strings.ToLower(value))
	return v != "" && v != "false" && v != "0" && v != "null"
}

func parseOutputFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	out := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '='); i > 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out, scanner.Err()
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	if name == "" {
		name = "artifact"
	}
	return name
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			return copyFile(path, target)
		})
	}
	return copyFile(src, dst)
}

func copyArtifactContents(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return out.Close()
}

func gitChangedFiles(ctx context.Context, repoRoot string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	files := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if path != "" {
			files = append(files, filepath.ToSlash(path))
		}
	}
	return files, scanner.Err()
}
