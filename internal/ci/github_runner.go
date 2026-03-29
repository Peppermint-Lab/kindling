package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubactions"
	"github.com/kindlingvm/kindling/internal/githubapi"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type GitHubRunnerProvisioner interface {
	Backend() string
	SupportsTarget(target githubactions.RunnerTarget) error
	Run(ctx context.Context, req GitHubRunnerProvisionRequest) error
}

type GitHubRunnerProvisionRequest struct {
	JITConfig  githubactions.JITConfig
	RunnerName string
	Target     githubactions.RunnerTarget
	LogLine    func(string)
}

type GitHubWorkflowJobEvent struct {
	Action         string
	RepoFullName   string
	OrgLogin       string
	WorkflowName   string
	JobName        string
	EventName      string
	WorkflowJobID  int64
	WorkflowRunID  int64
	RunAttempt     int32
	HTMLURL        string
	Labels         []string
	InstallationID int64
	Conclusion     string
}

type GitHubWorkflowJobHandleResult struct {
	Job            *queries.CiJob
	ShouldSchedule bool
	Ignored        bool
	Reason         string
}

func (s *JobService) HandleGitHubWorkflowJobEvent(ctx context.Context, event GitHubWorkflowJobEvent) (GitHubWorkflowJobHandleResult, error) {
	repo := githubapi.NormalizeRepo(event.RepoFullName)
	if repo == "" {
		return GitHubWorkflowJobHandleResult{Ignored: true, Reason: "missing repository"}, nil
	}
	if !hasRequiredGitHubRunnerLabels(event.Labels) {
		return GitHubWorkflowJobHandleResult{Ignored: true, Reason: "job is not targeted at Kindling runners"}, nil
	}

	project, err := s.q.ProjectFindByGitHubRepo(ctx, repo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GitHubWorkflowJobHandleResult{Ignored: true, Reason: "repository is not connected to a Kindling project"}, nil
		}
		return GitHubWorkflowJobHandleResult{}, err
	}

	integration, err := s.githubIntegrationForProject(ctx, project, ownerFromEvent(repo, event.OrgLogin))
	if err != nil {
		return GitHubWorkflowJobHandleResult{Ignored: true, Reason: err.Error()}, nil
	}

	job, err := s.q.CIJobFirstByExternalWorkflowJobID(ctx, event.WorkflowJobID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return GitHubWorkflowJobHandleResult{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		created, err := s.createGitHubWorkflowJob(ctx, project, integration, event)
		if err != nil {
			return GitHubWorkflowJobHandleResult{}, err
		}
		job = created
	}

	switch strings.TrimSpace(strings.ToLower(event.Action)) {
	case "queued":
		_ = s.log(ctx, job.ID, "info", "Received GitHub workflow_job queued event")
		s.publishProject(uuid.UUID(project.ID.Bytes))
		s.publishCIJob(uuid.UUID(job.ID.Bytes))
		return GitHubWorkflowJobHandleResult{Job: &job, ShouldSchedule: !isTerminalStatus(job.Status)}, nil
	case "in_progress":
		_ = s.log(ctx, job.ID, "info", "Received GitHub workflow_job in_progress event")
		if !isTerminalStatus(job.Status) {
			if err := s.q.CIJobMarkRunning(ctx, queries.CIJobMarkRunningParams{
				ID:               job.ID,
				WorkspaceDir:     job.WorkspaceDir,
				ExecutionBackend: firstNonEmpty(strings.TrimSpace(job.ExecutionBackend), "github_actions_runner"),
			}); err != nil {
				return GitHubWorkflowJobHandleResult{}, err
			}
		}
	case "completed":
		_ = s.log(ctx, job.ID, "info", fmt.Sprintf("Received GitHub workflow_job completed event (%s)", strings.TrimSpace(event.Conclusion)))
		if err := s.finishGitHubWorkflowJob(ctx, job, event.Conclusion); err != nil {
			return GitHubWorkflowJobHandleResult{}, err
		}
		s.cancelRunningJob(uuid.UUID(job.ID.Bytes))
	default:
		return GitHubWorkflowJobHandleResult{Ignored: true, Reason: "unsupported workflow_job action"}, nil
	}

	s.publishProject(uuid.UUID(project.ID.Bytes))
	s.publishCIJob(uuid.UUID(job.ID.Bytes))
	return GitHubWorkflowJobHandleResult{Job: &job}, nil
}

func (s *JobService) reconcileGitHubRunnerJob(ctx context.Context, job queries.CiJob) error {
	if isTerminalStatus(job.Status) {
		return nil
	}
	if job.CanceledAt.Valid {
		if err := s.q.CIJobMarkCanceled(ctx, job.ID); err != nil {
			return err
		}
		s.publishProject(uuid.UUID(job.ProjectID.Bytes))
		s.publishCIJob(uuid.UUID(job.ID.Bytes))
		return nil
	}
	job, err := s.q.CIJobClaimLease(ctx, queries.CIJobClaimLeaseParams{
		ID:           job.ID,
		ProcessingBy: pguuid.ToPgtype(s.serverID),
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	defer func() {
		_ = s.q.CIJobReleaseLease(context.Background(), queries.CIJobReleaseLeaseParams{
			ID:           job.ID,
			ProcessingBy: pguuid.ToPgtype(s.serverID),
		})
	}()

	project, err := s.q.ProjectFirstByID(ctx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load project for GitHub runner job: %w", err)
	}
	integration, err := s.githubIntegrationForJob(ctx, project, job)
	if err != nil {
		return s.failGitHubRunnerJob(ctx, job, err)
	}
	target, err := githubactions.ResolveRunnerTarget(parseRunnerLabels(job.RunnerLabels), integration.Metadata.DefaultLabels)
	if err != nil {
		return s.failGitHubRunnerJob(ctx, job, err)
	}
	if s.ghRunner == nil {
		return s.failGitHubRunnerJob(ctx, job, fmt.Errorf("GitHub runner provisioner is not configured"))
	}
	if err := s.ghRunner.SupportsTarget(target); err != nil {
		return s.failGitHubRunnerJob(ctx, job, err)
	}
	if s.ghClient == nil {
		return s.failGitHubRunnerJob(ctx, job, fmt.Errorf("GitHub runner client is not configured"))
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running[uuid.UUID(job.ID.Bytes)] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, uuid.UUID(job.ID.Bytes))
		s.mu.Unlock()
	}()

	_ = s.log(ctx, job.ID, "info", fmt.Sprintf("Preparing GitHub runner %s", strings.TrimSpace(job.RunnerName)))
	jitConfig, err := s.ghClient.GenerateJITConfig(runCtx, githubactions.JITConfigRequest{
		Integration: integration,
		RunnerName:  strings.TrimSpace(job.RunnerName),
		Labels:      target.Labels,
	})
	if err != nil {
		return s.failGitHubRunnerJob(ctx, job, err)
	}
	_ = s.log(ctx, job.ID, "info", "Received GitHub just-in-time runner configuration")
	if err := s.q.CIJobMarkRunning(ctx, queries.CIJobMarkRunningParams{
		ID:               job.ID,
		WorkspaceDir:     job.WorkspaceDir,
		ExecutionBackend: s.ghRunner.Backend(),
	}); err != nil {
		return err
	}
	s.publishProject(uuid.UUID(job.ProjectID.Bytes))
	s.publishCIJob(uuid.UUID(job.ID.Bytes))

	err = s.ghRunner.Run(runCtx, GitHubRunnerProvisionRequest{
		JITConfig:  jitConfig,
		RunnerName: strings.TrimSpace(job.RunnerName),
		Target:     target,
		LogLine: func(line string) {
			_ = s.log(context.Background(), job.ID, "info", line)
		},
	})
	current, fetchErr := s.q.CIJobFirstByID(ctx, job.ID)
	if fetchErr == nil && isTerminalStatus(current.Status) {
		return nil
	}
	if runCtx.Err() != nil {
		return nil
	}
	if err != nil {
		_ = s.ghClient.DeleteRunner(context.Background(), githubactions.DeleteRunnerRequest{
			Integration: integration,
			RunnerID:    jitConfig.RunnerID,
		})
		return s.failGitHubRunnerJob(ctx, job, err)
	}
	_ = s.log(ctx, job.ID, "info", "Runner session exited; awaiting GitHub completion webhook")
	s.publishProject(uuid.UUID(job.ProjectID.Bytes))
	s.publishCIJob(uuid.UUID(job.ID.Bytes))
	return nil
}

func (s *JobService) githubIntegrationForProject(ctx context.Context, project queries.Project, owner string) (githubactions.Integration, error) {
	rows, err := s.q.OrgProviderConnectionListByOrg(ctx, project.OrgID)
	if err != nil {
		return githubactions.Integration{}, fmt.Errorf("list org provider connections: %w", err)
	}
	integrations := make([]githubactions.Integration, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(strings.ToLower(row.Provider)) != "github" {
			continue
		}
		if s.cfg == nil {
			continue
		}
		plain, err := s.cfg.DecryptBytes(row.CredentialsCiphertext)
		if err != nil {
			continue
		}
		integration, err := githubactions.IntegrationFromConnection(row, plain)
		if err != nil {
			continue
		}
		integrations = append(integrations, integration)
	}
	if integration, ok := githubactions.ResolveIntegrationForOwner(integrations, owner); ok {
		return integration, nil
	}
	return githubactions.Integration{}, fmt.Errorf("no GitHub Actions runner integration is configured for %s", owner)
}

func (s *JobService) githubIntegrationForJob(ctx context.Context, project queries.Project, job queries.CiJob) (githubactions.Integration, error) {
	integration, err := s.githubIntegrationForProject(ctx, project, ownerFromEvent(job.ExternalRepo, ""))
	if err != nil {
		return githubactions.Integration{}, err
	}
	if job.ProviderConnectionID.Valid && integration.Connection.ID != job.ProviderConnectionID {
		rows, listErr := s.q.OrgProviderConnectionListByOrg(ctx, project.OrgID)
		if listErr != nil {
			return githubactions.Integration{}, listErr
		}
		for _, row := range rows {
			if row.ID != job.ProviderConnectionID {
				continue
			}
			plain, derr := s.cfg.DecryptBytes(row.CredentialsCiphertext)
			if derr != nil {
				return githubactions.Integration{}, derr
			}
			return githubactions.IntegrationFromConnection(row, plain)
		}
	}
	return integration, nil
}

func (s *JobService) createGitHubWorkflowJob(ctx context.Context, project queries.Project, integration githubactions.Integration, event GitHubWorkflowJobEvent) (queries.CiJob, error) {
	labelsJSON, err := json.Marshal(normalizeRunnerLabels(event.Labels))
	if err != nil {
		return queries.CiJob{}, err
	}
	inputsJSON, err := json.Marshal(map[string]string{})
	if err != nil {
		return queries.CiJob{}, err
	}
	jobID := uuid.New()
	runnerName := fmt.Sprintf("kindling-%d-%d", event.WorkflowRunID, event.WorkflowJobID)
	job, err := s.q.CIJobCreateGitHubRunner(ctx, queries.CIJobCreateGitHubRunnerParams{
		ID:                     pguuid.ToPgtype(jobID),
		ProjectID:              project.ID,
		Status:                 "queued",
		WorkflowName:           firstNonEmpty(strings.TrimSpace(event.WorkflowName), "GitHub Actions"),
		WorkflowFile:           "",
		SelectedJobID:          strings.TrimSpace(event.JobName),
		EventName:              strings.TrimSpace(event.EventName),
		InputValues:            inputsJSON,
		InputArchivePath:       "",
		ProviderConnectionID:   integration.Connection.ID,
		ExternalRepo:           githubapi.NormalizeRepo(event.RepoFullName),
		ExternalInstallationID: firstNonZero(event.InstallationID, integration.Metadata.InstallationID),
		ExternalWorkflowJobID:  event.WorkflowJobID,
		ExternalWorkflowRunID:  event.WorkflowRunID,
		ExternalRunAttempt:     event.RunAttempt,
		ExternalHtmlUrl:        strings.TrimSpace(event.HTMLURL),
		RunnerLabels:           labelsJSON,
		RunnerName:             runnerName,
		RequireMicrovm:         true,
		WorkspaceDir:           "",
		ErrorMessage:           "",
	})
	if err != nil {
		return queries.CiJob{}, err
	}
	return job, nil
}

func (s *JobService) finishGitHubWorkflowJob(ctx context.Context, job queries.CiJob, conclusion string) error {
	switch strings.TrimSpace(strings.ToLower(conclusion)) {
	case "", "success", "neutral":
		return s.q.CIJobMarkSuccessful(ctx, queries.CIJobMarkSuccessfulParams{
			ID:       job.ID,
			ExitCode: pgtype.Int4{Int32: 0, Valid: true},
		})
	case "cancelled", "canceled", "skipped":
		return s.q.CIJobMarkCanceled(ctx, job.ID)
	default:
		return s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
			ID:           job.ID,
			ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
			ErrorMessage: firstNonEmpty(strings.TrimSpace(job.ErrorMessage), fmt.Sprintf("GitHub workflow job concluded with %s", strings.TrimSpace(conclusion))),
		})
	}
}

func (s *JobService) failGitHubRunnerJob(ctx context.Context, job queries.CiJob, err error) error {
	if err == nil {
		return nil
	}
	_ = s.log(ctx, job.ID, "error", err.Error())
	if markErr := s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
		ID:           job.ID,
		ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
		ErrorMessage: err.Error(),
	}); markErr != nil {
		return markErr
	}
	s.publishProject(uuid.UUID(job.ProjectID.Bytes))
	s.publishCIJob(uuid.UUID(job.ID.Bytes))
	return nil
}

func (s *JobService) cancelRunningJob(jobID uuid.UUID) {
	s.mu.Lock()
	cancel := s.running[jobID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func hasRequiredGitHubRunnerLabels(labels []string) bool {
	seenSelfHosted := false
	seenKindling := false
	for _, label := range normalizeRunnerLabels(labels) {
		switch label {
		case githubactions.LabelSelfHosted:
			seenSelfHosted = true
		case githubactions.LabelKindling:
			seenKindling = true
		}
	}
	return seenSelfHosted && seenKindling
}

func normalizeRunnerLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(strings.ToLower(label))
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

func parseRunnerLabels(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var labels []string
	_ = json.Unmarshal(raw, &labels)
	return normalizeRunnerLabels(labels)
}

func ownerFromEvent(repo, orgLogin string) string {
	if strings.TrimSpace(orgLogin) != "" {
		return strings.TrimSpace(orgLogin)
	}
	repo = githubapi.NormalizeRepo(repo)
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func firstNonZero(values ...int64) int64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}
