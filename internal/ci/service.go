package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type JobService struct {
	q        *queries.Queries
	serverID uuid.UUID
	resolver WorkflowResolver
	compiler WorkflowCompiler
	runner   WorkflowRunner

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
}

func NewJobService(q *queries.Queries, serverID uuid.UUID) *JobService {
	return &JobService{
		q:        q,
		serverID: serverID,
		resolver: NewFSWorkflowResolver(),
		compiler: NewStaticWorkflowCompiler(),
		runner:   NewPreferredWorkflowRunner(),
		running:  map[uuid.UUID]context.CancelFunc{},
	}
}

type CreateJobRequest struct {
	ProjectID     uuid.UUID
	WorkflowRef   string
	JobID         string
	EventName     string
	Inputs        map[string]string
	ArchiveBase64 string
}

func (s *JobService) CreateLocalWorkflowJob(ctx context.Context, req CreateJobRequest) (queries.CiJob, error) {
	inputsJSON, err := json.Marshal(req.Inputs)
	if err != nil {
		return queries.CiJob{}, err
	}
	jobID := uuid.New()
	job, err := s.q.CIJobCreate(ctx, queries.CIJobCreateParams{
		ID:               pguuid.ToPgtype(jobID),
		ProjectID:        pguuid.ToPgtype(req.ProjectID),
		Status:           "queued",
		Source:           "local_workflow_run",
		WorkflowName:     strings.TrimSpace(req.WorkflowRef),
		WorkflowFile:     "",
		SelectedJobID:    strings.TrimSpace(req.JobID),
		EventName:        strings.TrimSpace(req.EventName),
		InputValues:      inputsJSON,
		InputArchivePath: "",
		WorkspaceDir:     "",
		ErrorMessage:     "",
	})
	if err != nil {
		return queries.CiJob{}, err
	}
	if strings.TrimSpace(req.ArchiveBase64) != "" {
		dir, err := archiveStorageDir()
		if err != nil {
			return queries.CiJob{}, err
		}
		archivePath := filepath.Join(dir, jobID.String()+".tar.gz")
		if err := SaveArchiveFromBase64(req.ArchiveBase64, archivePath); err != nil {
			return queries.CiJob{}, err
		}
		if err := s.q.CIJobUpdateInputArchivePath(ctx, queries.CIJobUpdateInputArchivePathParams{
			ID:               pguuid.ToPgtype(jobID),
			InputArchivePath: archivePath,
		}); err != nil {
			return queries.CiJob{}, err
		}
		job.InputArchivePath = archivePath
	}
	return job, nil
}

func (s *JobService) Cancel(ctx context.Context, jobID uuid.UUID) error {
	s.mu.Lock()
	cancel := s.running[jobID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.q.CIJobMarkCanceled(ctx, pguuid.ToPgtype(jobID))
}

func (s *JobService) Reconcile(ctx context.Context, jobID uuid.UUID) error {
	job, err := s.q.CIJobFirstByID(ctx, pguuid.ToPgtype(jobID))
	if err != nil {
		return fmt.Errorf("fetch ci job: %w", err)
	}
	if isTerminalStatus(job.Status) {
		return nil
	}
	if job.CanceledAt.Valid {
		return s.q.CIJobMarkCanceled(ctx, job.ID)
	}
	job, err = s.q.CIJobClaimLease(ctx, queries.CIJobClaimLeaseParams{
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

	workDir, cleanup, err := s.prepareWorkspace(job)
	if err != nil {
		_ = s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
			ID:           job.ID,
			ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
			ErrorMessage: err.Error(),
		})
		return nil
	}
	defer cleanup()

	if err := s.q.CIJobMarkRunning(ctx, queries.CIJobMarkRunningParams{
		ID:           job.ID,
		WorkspaceDir: workDir,
	}); err != nil {
		return err
	}
	_ = s.log(ctx, job.ID, "info", fmt.Sprintf("Starting workflow %s", job.WorkflowName))

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running[jobID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, jobID)
		s.mu.Unlock()
	}()

	workflow, err := s.resolver.Resolve(workDir, job.WorkflowName)
	if err != nil {
		_ = s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
			ID:           job.ID,
			ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
			ErrorMessage: err.Error(),
		})
		return nil
	}
	plan, err := s.compiler.Compile(CompileRequest{
		Workflow: workflow,
		JobID:    job.SelectedJobID,
		Event:    job.EventName,
		Inputs:   decodeInputMap(job.InputValues),
		RepoRoot: workDir,
	})
	if err != nil {
		_ = s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
			ID:           job.ID,
			ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
			ErrorMessage: err.Error(),
		})
		return nil
	}
	logWriter := &lineLogWriter{fn: func(line string) {
		_ = s.log(context.Background(), job.ID, "info", line)
	}}
	result, runErr := s.runner.Run(runCtx, plan, RunOptions{
		Stdout: logWriter,
		Stderr: logWriter,
	})
	logWriter.Flush()
	_ = s.replaceArtifacts(ctx, job.ID, result.Artifacts)
	switch {
	case runCtx.Err() != nil || job.CanceledAt.Valid:
		_ = s.log(ctx, job.ID, "info", "Job canceled")
		_ = s.q.CIJobMarkCanceled(ctx, job.ID)
	case runErr != nil:
		_ = s.log(ctx, job.ID, "error", runErr.Error())
		_ = s.q.CIJobMarkFailed(ctx, queries.CIJobMarkFailedParams{
			ID:           job.ID,
			ExitCode:     pgtype.Int4{Int32: 1, Valid: true},
			ErrorMessage: runErr.Error(),
		})
	default:
		_ = s.log(ctx, job.ID, "info", "Job completed successfully")
		_ = s.q.CIJobMarkSuccessful(ctx, queries.CIJobMarkSuccessfulParams{
			ID:       job.ID,
			ExitCode: pgtype.Int4{Int32: 0, Valid: true},
		})
	}
	return nil
}

func (s *JobService) prepareWorkspace(job queries.CiJob) (string, func(), error) {
	if strings.TrimSpace(job.InputArchivePath) == "" {
		return "", nil, fmt.Errorf("ci job has no input archive")
	}
	root, err := archiveStorageDir()
	if err != nil {
		return "", nil, err
	}
	workDir := filepath.Join(root, pguuid.ToString(job.ID)+"-workspace")
	_ = os.RemoveAll(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", nil, err
	}
	if err := ExtractArchiveToDir(job.InputArchivePath, workDir); err != nil {
		return "", nil, err
	}
	return workDir, func() {}, nil
}

func (s *JobService) replaceArtifacts(ctx context.Context, jobID pgtype.UUID, artifacts []ArtifactInfo) error {
	if err := s.q.CIJobArtifactDeleteByJobID(ctx, jobID); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := s.q.CIJobArtifactCreate(ctx, queries.CIJobArtifactCreateParams{
			ID:      pguuid.ToPgtype(uuid.New()),
			CiJobID: jobID,
			Name:    artifact.Name,
			Path:    artifact.Path,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *JobService) log(ctx context.Context, jobID pgtype.UUID, level, message string) error {
	return s.q.CIJobLogCreate(ctx, queries.CIJobLogCreateParams{
		ID:      pguuid.ToPgtype(uuid.New()),
		CiJobID: jobID,
		Message: message,
		Level:   level,
	})
}

func isTerminalStatus(status string) bool {
	switch status {
	case "successful", "failed", "canceled":
		return true
	default:
		return false
	}
}

func decodeInputMap(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]string
	_ = json.Unmarshal(raw, &out)
	return out
}

type lineLogWriter struct {
	mu  sync.Mutex
	buf strings.Builder
	fn  func(string)
}

func (w *lineLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			line := strings.TrimRight(w.buf.String(), "\r")
			w.buf.Reset()
			if strings.TrimSpace(line) != "" && w.fn != nil {
				w.fn(line)
			}
			continue
		}
		w.buf.WriteByte(b)
	}
	return len(p), nil
}

func (w *lineLogWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(w.buf.String()) != "" && w.fn != nil {
		w.fn(strings.TrimRight(w.buf.String(), "\r"))
	}
	w.buf.Reset()
}
