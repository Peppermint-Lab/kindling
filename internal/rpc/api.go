package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/reconciler"
	rpcauth "github.com/kindlingvm/kindling/internal/rpc/auth"
	"github.com/kindlingvm/kindling/internal/rpc/deployments"
	"github.com/kindlingvm/kindling/internal/rpc/domains"
	"github.com/kindlingvm/kindling/internal/rpc/projects"
	"github.com/kindlingvm/kindling/internal/rpc/servers"
	"github.com/kindlingvm/kindling/internal/rpc/volumes"
)

// API provides REST endpoints for the dashboard.
type API struct {
	q                    *queries.Queries
	cfg                  *config.Manager
	dashboardEvents      *DashboardEventBroker
	deploymentReconciler *reconciler.Scheduler
}

// NewAPI creates a new API handler. cfg supplies DB-backed secrets (e.g. GitHub token).
// dashboardEvents may be nil; in that case GET /api/events returns 503.
func NewAPI(q *queries.Queries, cfg *config.Manager, dashboardEvents *DashboardEventBroker) *API {
	return &API{q: q, cfg: cfg, dashboardEvents: dashboardEvents}
}

// SetDeploymentReconciler configures the reconciler used for immediate preview
// cleanup actions exposed via the dashboard APIs.
func (a *API) SetDeploymentReconciler(r *reconciler.Scheduler) {
	a.deploymentReconciler = r
}

func (a *API) gitHubToken() string {
	if a.cfg == nil {
		return ""
	}
	s := a.cfg.Snapshot()
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.GitHubToken)
}

// Register mounts all API routes on the given mux.
// Route registration is delegated to domain-specific sub-packages.
func (a *API) Register(mux *http.ServeMux) {
	// Projects sub-package: CRUD, secrets, previews, git, usage, meta.
	(&projects.Handlers{
		GetMeta: a.getMeta, PutMeta: a.putMeta,
		ListProjects: a.listProjects, CreateProject: a.createProject,
		GetProject: a.getProject, PatchProject: a.patchProject,
		DeleteProject: a.deleteProject,
		ListProjectSecrets: a.listProjectSecrets, UpsertProjectSecret: a.upsertProjectSecret,
		DeleteProjectSecret: a.deleteProjectSecret,
		ListProjectPreviews: a.listProjectPreviews, RedeployProjectPreview: a.redeployProjectPreview,
		DeleteProjectPreview: a.deleteProjectPreview,
		GetGitHubSetup: a.getGitHubSetup, GitHead: a.gitHead,
		RotateWebhookSecret: a.rotateWebhookSecret,
		GetProjectUsageCurrent: a.getProjectUsageCurrent, GetProjectUsageHistory: a.getProjectUsageHistory,
	}).RegisterRoutes(mux)

	// Volumes sub-package: volume CRUD and operations.
	(&volumes.Handlers{
		GetProjectVolume: a.getProjectVolume, PutProjectVolume: a.putProjectVolume,
		DeleteProjectVolume: a.deleteProjectVolume,
		ListProjectVolumeBackups: a.listProjectVolumeBackups,
		PostProjectVolumeBackup: a.postProjectVolumeBackup,
		PostProjectVolumeRestore: a.postProjectVolumeRestore,
		PostProjectVolumeMove: a.postProjectVolumeMove,
		PostProjectVolumeRepair: a.postProjectVolumeRepair,
	}).RegisterRoutes(mux)

	// Deployments sub-package: deployment CRUD, logs, SSE stream, live migration.
	depH := &deployments.Handler{Q: a.q, DeploymentReconciler: a.deploymentReconciler}
	depH.RegisterRoutes(mux)
	depH.RegisterMigrationRoutes(mux)

	// Domains sub-package: custom domain management.
	(&domains.Handler{Q: a.q}).RegisterRoutes(mux)

	// Servers sub-package: server listing, drain, activate.
	(&servers.Handler{Q: a.q}).RegisterRoutes(mux)

	// Auth sub-package: authentication, API keys, providers, SSE events.
	(&rpcauth.Handlers{
		AuthBootstrapStatus: a.authBootstrapStatus, AuthSession: a.authSession,
		AuthBootstrap: a.authBootstrap, AuthLogin: a.authLogin,
		AuthLogout: a.authLogout, AuthSwitchOrg: a.authSwitchOrg,
		ListAPIKeys: a.listAPIKeys, CreateAPIKey: a.createAPIKey, RevokeAPIKey: a.revokeAPIKey,
		ListOrgProviders: a.listOrgProviderConnections, CreateOrgProvider: a.createOrgProviderConnection,
		DeleteOrgProvider: a.deleteOrgProviderConnection,
		ListPublicAuthProviders: a.listPublicAuthProviders, ListAdminAuthProviders: a.listAdminAuthProviders,
		PutAdminAuthProvider: a.putAdminAuthProvider,
		ListAuthIdentities: a.listAuthIdentities,
		StartExternalAuth: a.startExternalAuth, LinkExternalAuth: a.linkExternalAuth,
		ExternalAuthCallback: a.externalAuthCallback,
		StreamDashboardEvents: a.streamDashboardEvents,
	}).RegisterRoutes(mux)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func parseUUID(s string) (pgtype.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
