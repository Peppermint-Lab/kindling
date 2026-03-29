package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/githubapi"
	"github.com/kindlingvm/kindling/internal/rpc/rpcutil"
	"github.com/kindlingvm/kindling/internal/shared/pguuid"
)

type ciJobOut struct {
	ID               string            `json:"id"`
	ProjectID        string            `json:"project_id"`
	ProjectName      string            `json:"project_name,omitempty"`
	Status           string            `json:"status"`
	Source           string            `json:"source"`
	WorkflowName     string            `json:"workflow_name"`
	WorkflowFile     string            `json:"workflow_file"`
	SelectedJobID    string            `json:"selected_job_id,omitempty"`
	EventName        string            `json:"event_name,omitempty"`
	Inputs           map[string]string `json:"inputs,omitempty"`
	RequireMicroVM   bool              `json:"require_microvm"`
	ExecutionBackend string            `json:"execution_backend,omitempty"`
	ExitCode         *int32            `json:"exit_code,omitempty"`
	ErrorMessage     string            `json:"error_message,omitempty"`
	StartedAt        *string           `json:"started_at,omitempty"`
	FinishedAt       *string           `json:"finished_at,omitempty"`
	CanceledAt       *string           `json:"canceled_at,omitempty"`
	CreatedAt        *string           `json:"created_at,omitempty"`
	UpdatedAt        *string           `json:"updated_at,omitempty"`
}

type ciJobArtifactOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	CreatedAt *string `json:"created_at,omitempty"`
}

type ciWorkflowOut struct {
	Stem     string             `json:"stem"`
	Name     string             `json:"name"`
	File     string             `json:"file"`
	Triggers map[string]bool    `json:"triggers,omitempty"`
	Inputs   map[string]string  `json:"inputs,omitempty"`
	Jobs     []ciWorkflowJobOut `json:"jobs"`
}

type ciWorkflowJobOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func ciJobToOut(job queries.CiJob) ciJobOut {
	var inputs map[string]string
	_ = json.Unmarshal(job.InputValues, &inputs)
	var exitCode *int32
	if job.ExitCode.Valid {
		v := job.ExitCode.Int32
		exitCode = &v
	}
	return ciJobOut{
		ID:               pguuid.ToString(job.ID),
		ProjectID:        pguuid.ToString(job.ProjectID),
		Status:           job.Status,
		Source:           job.Source,
		WorkflowName:     job.WorkflowName,
		WorkflowFile:     job.WorkflowFile,
		SelectedJobID:    job.SelectedJobID,
		EventName:        job.EventName,
		Inputs:           inputs,
		RequireMicroVM:   job.RequireMicrovm,
		ExecutionBackend: strings.TrimSpace(job.ExecutionBackend),
		ExitCode:         exitCode,
		ErrorMessage:     strings.TrimSpace(job.ErrorMessage),
		StartedAt:        rpcutil.FormatTS(job.StartedAt),
		FinishedAt:       rpcutil.FormatTS(job.FinishedAt),
		CanceledAt:       rpcutil.FormatTS(job.CanceledAt),
		CreatedAt:        rpcutil.FormatTS(job.CreatedAt),
		UpdatedAt:        rpcutil.FormatTS(job.UpdatedAt),
	}
}

func ciJobToOutWithProjectName(job queries.CiJob, projectName string) ciJobOut {
	out := ciJobToOut(job)
	out.ProjectName = strings.TrimSpace(projectName)
	return out
}

func ciJobListRowToOut(row queries.CIJobFindRecentWithProjectForOrgRow) ciJobOut {
	return ciJobToOutWithProjectName(queries.CiJob{
		ID:               row.ID,
		ProjectID:        row.ProjectID,
		Status:           row.Status,
		Source:           row.Source,
		WorkflowName:     row.WorkflowName,
		WorkflowFile:     row.WorkflowFile,
		SelectedJobID:    row.SelectedJobID,
		EventName:        row.EventName,
		InputValues:      row.InputValues,
		InputArchivePath: row.InputArchivePath,
		RequireMicrovm:   row.RequireMicrovm,
		ExecutionBackend: row.ExecutionBackend,
		WorkspaceDir:     row.WorkspaceDir,
		ProcessingBy:     row.ProcessingBy,
		ExitCode:         row.ExitCode,
		ErrorMessage:     row.ErrorMessage,
		StartedAt:        row.StartedAt,
		FinishedAt:       row.FinishedAt,
		CanceledAt:       row.CanceledAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}, row.ProjectName)
}

func (a *API) listProjectCIJobs(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	if _, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	}); err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	rows, err := a.q.CIJobFindByProjectID(r.Context(), projectID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_ci_jobs", err)
		return
	}
	out := make([]ciJobOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, ciJobToOut(row))
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (a *API) listAllCIJobs(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	limit := int32(100)
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	rows, err := a.q.CIJobFindRecentWithProjectForOrg(r.Context(), queries.CIJobFindRecentWithProjectForOrgParams{
		OrgID: p.OrganizationID,
		Limit: limit,
	})
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "list_ci_jobs", err)
		return
	}
	out := make([]ciJobOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, ciJobListRowToOut(row))
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (a *API) listCIWorkflows(w http.ResponseWriter, r *http.Request) {
	if _, ok := rpcutil.MustPrincipal(w, r); !ok {
		return
	}
	resolver := ci.NewFSWorkflowResolver()
	repoRoot, err := resolver.FindRepoRoot(".")
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusNotFound, "ci_workflows", err)
		return
	}
	workflows, err := resolver.List(repoRoot)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "ci_workflows", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, workflowsToOut(workflows))
}

func (a *API) listProjectCIWorkflows(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		ref = "main"
	}
	workflows, err := a.listProjectRepoWorkflows(r.Context(), strings.TrimSpace(project.GithubRepository), ref)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusBadGateway, "ci_workflows", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, workflowsToOut(workflows))
}

func workflowsToOut(workflows []ci.Workflow) []ciWorkflowOut {
	out := make([]ciWorkflowOut, 0, len(workflows))
	for _, wf := range workflows {
		jobIDs := make([]string, 0, len(wf.Jobs))
		for jobID := range wf.Jobs {
			jobIDs = append(jobIDs, jobID)
		}
		sort.Strings(jobIDs)
		jobs := make([]ciWorkflowJobOut, 0, len(jobIDs))
		for _, jobID := range jobIDs {
			jobs = append(jobs, ciWorkflowJobOut{ID: jobID, Name: wf.Jobs[jobID].Name})
		}
		inputs := map[string]string{}
		for name, spec := range wf.DispatchInput {
			inputs[name] = spec.Default
		}
		out = append(out, ciWorkflowOut{
			Stem: wf.Stem,
			Name: wf.Name,
			File: wf.Filename,
			Triggers: map[string]bool{
				"push":              wf.Triggers.Push,
				"pull_request":      wf.Triggers.PullRequest,
				"workflow_dispatch": wf.Triggers.WorkflowDispatch,
			},
			Inputs: inputs,
			Jobs:   jobs,
		})
	}
	return out
}

func (a *API) createProjectCIJob(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	projectID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid project id")
		return
	}
	project, err := a.q.ProjectFirstByIDAndOrg(r.Context(), queries.ProjectFirstByIDAndOrgParams{
		ID:    projectID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	var req struct {
		Workflow       string            `json:"workflow"`
		Job            string            `json:"job"`
		Event          string            `json:"event"`
		Inputs         map[string]string `json:"inputs"`
		ArchiveBase64  string            `json:"archive_base64"`
		RequireMicroVM *bool             `json:"require_microvm"`
		Ref            string            `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_json", "malformed JSON body")
		return
	}
	if strings.TrimSpace(req.Workflow) == "" {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "validation_error", "workflow is required")
		return
	}
	if strings.TrimSpace(req.ArchiveBase64) == "" {
		ref := strings.TrimSpace(req.Ref)
		if ref == "" {
			ref = "main"
		}
		archiveBase64, archiveErr := a.downloadProjectArchiveBase64(r.Context(), strings.TrimSpace(project.GithubRepository), ref)
		if archiveErr != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusBadGateway, "create_ci_job", archiveErr)
			return
		}
		req.ArchiveBase64 = archiveBase64
	}
	eventName := strings.TrimSpace(req.Event)
	if eventName == "" {
		eventName = "workflow_dispatch"
	}
	createReq := ci.CreateJobRequest{
		ProjectID:      uuid.UUID(projectID.Bytes),
		WorkflowRef:    req.Workflow,
		JobID:          req.Job,
		EventName:      eventName,
		Inputs:         req.Inputs,
		ArchiveBase64:  req.ArchiveBase64,
		RequireMicroVM: req.RequireMicroVM == nil || *req.RequireMicroVM,
	}
	var job queries.CiJob
	if a.ciJobService != nil {
		job, err = a.ciJobService.CreateLocalWorkflowJob(r.Context(), createReq)
	} else {
		job, err = ci.CreateQueuedWorkflowJob(r.Context(), a.q, createReq)
	}
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "create_ci_job", err)
		return
	}
	if a.dashboardEvents != nil {
		projectUUID := uuid.UUID(job.ProjectID.Bytes)
		jobUUID := uuid.UUID(job.ID.Bytes)
		a.dashboardEvents.PublishMany(
			TopicCIJobs,
			TopicProject(projectUUID),
			TopicProjectCIJobs(projectUUID),
			TopicCIJob(jobUUID),
		)
	}
	if a.ciJobReconciler != nil {
		a.ciJobReconciler.ScheduleNow(uuid.UUID(job.ID.Bytes))
	}
	rpcutil.WriteJSON(w, http.StatusCreated, ciJobToOut(job))
}

func (a *API) downloadProjectArchiveBase64(ctx context.Context, repo, ref string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("project has no GitHub repository")
	}
	tarball, err := githubapi.DownloadTarball(ctx, nil, a.gitHubToken(), repo, ref)
	if err != nil {
		return "", err
	}
	defer tarball.Close()
	return ci.RepackGitHubTarballBase64(tarball)
}

func (a *API) listProjectRepoWorkflows(ctx context.Context, repo, ref string) ([]ci.Workflow, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("project has no GitHub repository")
	}
	tarball, err := githubapi.DownloadTarball(ctx, nil, a.gitHubToken(), repo, ref)
	if err != nil {
		return nil, err
	}
	defer tarball.Close()
	tmpDir, err := os.MkdirTemp("", "kindling-ci-workflows-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	if err := ci.ExtractGitHubTarballToDir(tarball, tmpDir); err != nil {
		return nil, err
	}
	resolver := ci.NewFSWorkflowResolver()
	workflows, err := resolver.List(tmpDir)
	if err != nil {
		return nil, err
	}
	return workflows, nil
}

func (a *API) getCIJob(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	jobID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid ci job id")
		return
	}
	job, err := a.q.CIJobFirstByIDAndOrg(r.Context(), queries.CIJobFirstByIDAndOrgParams{
		ID:    jobID,
		OrgID: p.OrganizationID,
	})
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "ci job not found")
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, ciJobToOut(job))
}

func (a *API) getCIJobLogs(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	job, ok := a.loadCIJobForOrg(w, r, p.OrganizationID)
	if !ok {
		return
	}
	rows, err := a.q.CIJobLogsByJobID(r.Context(), job.ID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "ci_job_logs", err)
		return
	}
	rpcutil.WriteJSON(w, http.StatusOK, rows)
}

func (a *API) getCIJobArtifacts(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	job, ok := a.loadCIJobForOrg(w, r, p.OrganizationID)
	if !ok {
		return
	}
	rows, err := a.q.CIJobArtifactsByJobID(r.Context(), job.ID)
	if err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "ci_job_artifacts", err)
		return
	}
	out := make([]ciJobArtifactOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, ciJobArtifactOut{
			ID:        pguuid.ToString(row.ID),
			Name:      row.Name,
			Path:      row.Path,
			CreatedAt: rpcutil.FormatTS(row.CreatedAt),
		})
	}
	rpcutil.WriteJSON(w, http.StatusOK, out)
}

func (a *API) cancelCIJob(w http.ResponseWriter, r *http.Request) {
	p, ok := rpcutil.MustPrincipal(w, r)
	if !ok {
		return
	}
	if !rpcutil.RequireOrgAdmin(w, p) {
		return
	}
	job, ok := a.loadCIJobForOrg(w, r, p.OrganizationID)
	if !ok {
		return
	}
	if a.ciJobService != nil {
		if err := a.ciJobService.Cancel(r.Context(), uuid.UUID(job.ID.Bytes)); err != nil {
			rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "cancel_ci_job", err)
			return
		}
	} else if err := a.q.CIJobMarkCanceled(r.Context(), job.ID); err != nil {
		rpcutil.WriteAPIErrorFromErr(w, http.StatusInternalServerError, "cancel_ci_job", err)
		return
	} else if a.dashboardEvents != nil {
		jobID := uuid.UUID(job.ID.Bytes)
		projectID := uuid.UUID(job.ProjectID.Bytes)
		a.dashboardEvents.PublishMany(
			TopicCIJobs,
			TopicProject(projectID),
			TopicProjectCIJobs(projectID),
			TopicCIJob(jobID),
		)
	}
	rpcutil.WriteJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

func (a *API) loadCIJobForOrg(w http.ResponseWriter, r *http.Request, orgID pgtype.UUID) (queries.CiJob, bool) {
	jobID, err := rpcutil.ParseUUID(r.PathValue("id"))
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusBadRequest, "invalid_id", "invalid ci job id")
		return queries.CiJob{}, false
	}
	job, err := a.q.CIJobFirstByIDAndOrg(r.Context(), queries.CIJobFirstByIDAndOrgParams{
		ID:    jobID,
		OrgID: orgID,
	})
	if err != nil {
		rpcutil.WriteAPIError(w, http.StatusNotFound, "not_found", "ci job not found")
		return queries.CiJob{}, false
	}
	return job, true
}
