const API_BASE = import.meta.env.VITE_API_URL || "http://localhost:8080"

export class APIError extends Error {
  status: number
  code?: string
  body?: string

  constructor(message: string, status: number, code?: string, body?: string) {
    super(message)
    this.name = "APIError"
    this.status = status
    this.code = code
    this.body = body
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  // Never send Content-Type on bodyless GET/HEAD (and similar): it turns the request
  // into a CORS preflight; Safari then often surfaces failures as "access control checks".
  const headers = new Headers(init?.headers)
  const hasBody = init?.body != null && init.body !== ""
  if (hasBody && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json")
  }
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    credentials: "include",
    headers,
  })
  const bodyText = await res.text()
  if (!res.ok) {
    let message = bodyText || res.statusText
    let code: string | undefined
    try {
      const j = JSON.parse(bodyText) as { error?: string; code?: string }
      if (j.error) message = j.error
      code = j.code
    } catch {
      /* plain text */
    }
    throw new APIError(message, res.status, code, bodyText)
  }
  if (res.status === 204) return undefined as T
  return JSON.parse(bodyText) as T
}

export type Project = {
  id: string
  org_id?: string
  name: string
  github_repository: string
  dockerfile_path: string
  root_directory: string
  desired_instance_count?: number
  build_only_on_root_changes?: boolean
  created_at: string
  updated_at: string
}

export type ProjectVolume = {
  id: string
  project_id: string
  server_id?: string | null
  attached_vm_id?: string | null
  mount_path: string
  size_gb: number
  filesystem: string
  status: string
  last_error?: string
  created_at?: string | null
  updated_at?: string | null
}

export type ProjectSecret = {
  id: string
  name: string
  created_at?: string | null
  updated_at?: string | null
}

export type AuthUser = {
  id: string
  email: string
  display_name: string
}

export type AuthOrganization = {
  id: string
  name: string
  slug: string
}

export type AuthSession =
  | { authenticated: false }
  | {
      authenticated: true
      user: AuthUser
      platform_admin: boolean
      organization: AuthOrganization
      role: string
      organizations: AuthOrganization[]
    }

export type AuthPublicProvider = {
  provider: "github" | "oidc"
  display_name: string
}

export type AuthAdminProvider = {
  provider: "github" | "oidc"
  display_name: string
  enabled: boolean
  configured: boolean
  client_id: string
  has_client_secret: boolean
  issuer_url: string
  scopes: string
  callback_url: string
  created_at: string
  updated_at: string
}

export type AuthIdentity = {
  provider: "github" | "oidc"
  provider_login: string
  provider_email: string
  provider_display_name: string
  created_at: string
  updated_at: string
  last_login_at?: string | null
}

export type Deployment = {
  id: string
  project_id: string
  build_id?: string | null
  image_id?: string | null
  vm_id?: string | null
  github_commit: string
  deployment_kind?: string
  github_branch?: string
  preview_environment_id?: string
  running_at?: string | null
  stopped_at?: string | null
  failed_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  build_status?: string
  phase: string
  desired_instance_count?: number
  running_instance_count?: number
  blocked_reason?: string
  persistent_volume?: DeploymentPersistentVolume | null
  reachable?: DeploymentReachability | null
}

export type DeploymentPersistentVolume = {
  id: string
  project_id: string
  server_id?: string | null
  attached_vm_id?: string | null
  mount_path: string
  size_gb: number
  filesystem: string
  status: string
  last_error?: string
}

export type DeploymentPublicEndpoint = {
  domain: string
  public_url: string
  redirect_to?: string
  redirect_status_code?: number
  proxies_to_deployment?: boolean
}

export type DeploymentReachability = {
  public_url?: string
  runtime_url?: string
  domain?: string
  vm_ip?: string
  port?: number
  proxies_to_deployment?: boolean
  public_endpoints?: DeploymentPublicEndpoint[]
}

export type DeploymentListItem = Deployment & {
  project_name: string
}

export type BuildLog = {
  id: string
  build_id: string
  message: string
  level: string
  created_at: string
}

export type Server = {
  id: string
  hostname: string
  internal_ip: string
  ip_range?: string
  status: string
  last_heartbeat_at: string
  created_at: string
  updated_at?: string
  /** Non-deleted deployment_instances with this server_id */
  instance_count?: number
  active_instance_count?: number
  running_instance_count?: number
  health?: "healthy" | "degraded" | "stale" | "unknown"
  heartbeat_health?: "healthy" | "stale" | "unknown"
  heartbeat_age_seconds?: number
  runtime?: string
  enabled_components?: string[]
  components?: ServerComponent[]
}

export type ServerComponent = {
  component: "api" | "edge" | "worker" | "usage_poller"
  enabled: boolean
  health: "healthy" | "degraded" | "stale" | "unknown"
  raw_status?: "healthy" | "degraded"
  observed_at?: string
  last_success_at?: string
  last_error_at?: string
  last_error_message?: string
  metadata?: Record<string, unknown>
}

export type ServerInstanceDetail = {
  deployment_instance_id: string
  deployment_id: string
  project_id: string
  project_name: string
  vm_id?: string
  role: string
  status: string
  created_at?: string
  updated_at?: string
  sampled_at?: string
  sample_age_seconds?: number
  resource_health: "fresh" | "stale" | "missing"
  cpu_percent?: number
  memory_rss_bytes: number
  disk_read_bytes: number
  disk_write_bytes: number
  source?: string
  migration_id?: string
  migration_state?: string
  migration_failure?: string
}

export type ServerDetail = {
  summary: Server
  instances: ServerInstanceDetail[]
  volumes: ServerVolumeDetail[]
}

export type ServerVolumeDetail = {
  id: string
  project_id: string
  project_name: string
  server_id?: string | null
  attached_vm_id?: string | null
  mount_path: string
  size_gb: number
  filesystem: string
  status: string
  last_error?: string
}

export type DeploymentInstanceMigration = {
  id: string
  deployment_instance_id: string
  source_server_id: string
  destination_server_id: string
  source_vm_id: string
  state: string
  mode: string
  receive_addr?: string
  destination_runtime_url?: string
  failure_code?: string
  failure_message?: string
  started_at?: string
  completed_at?: string
  failed_at?: string
  aborted_at?: string
}

export type UsageInstance = {
  deployment_instance_id: string
  sampled_at?: string
  cpu_percent?: number
  memory_rss_bytes: number
  disk_read_bytes: number
  disk_write_bytes: number
  source: string
}

export type UsageCurrent = {
  instances: UsageInstance[]
  summary: {
    memory_rss_bytes_total: number
    cpu_percent_avg: number | null
    http_requests_15m: number
    http_status_2xx_15m: number
    http_status_4xx_15m: number
    http_status_5xx_15m: number
    http_bytes_in_15m: number
    http_bytes_out_15m: number
  }
}

export type UsageHistory = {
  window: string
  resource: Array<{
    bucket_start: string
    memory_rss_bytes_max: number
    cpu_percent_avg: number
  }>
  http: Array<{
    bucket_start: string
    request_count: number
    status_2xx: number
    status_4xx: number
    status_5xx: number
    bytes_in: number
    bytes_out: number
  }>
}

/** Go json.Marshal encodes nil slices as null; normalize so UI never calls .map on null. */
function parseUsageCurrent(raw: unknown): UsageCurrent {
  const summaryDefaults: UsageCurrent["summary"] = {
    memory_rss_bytes_total: 0,
    cpu_percent_avg: null,
    http_requests_15m: 0,
    http_status_2xx_15m: 0,
    http_status_4xx_15m: 0,
    http_status_5xx_15m: 0,
    http_bytes_in_15m: 0,
    http_bytes_out_15m: 0,
  }
  if (!raw || typeof raw !== "object") {
    return { instances: [], summary: summaryDefaults }
  }
  const o = raw as Record<string, unknown>
  const instances = Array.isArray(o.instances) ? (o.instances as UsageCurrent["instances"]) : []
  let summary = summaryDefaults
  if (o.summary && typeof o.summary === "object") {
    const s = o.summary as Record<string, unknown>
    const cpu = s.cpu_percent_avg
    summary = {
      memory_rss_bytes_total:
        typeof s.memory_rss_bytes_total === "number" ? s.memory_rss_bytes_total : 0,
      cpu_percent_avg: typeof cpu === "number" && !Number.isNaN(cpu) ? cpu : null,
      http_requests_15m: typeof s.http_requests_15m === "number" ? s.http_requests_15m : 0,
      http_status_2xx_15m: typeof s.http_status_2xx_15m === "number" ? s.http_status_2xx_15m : 0,
      http_status_4xx_15m: typeof s.http_status_4xx_15m === "number" ? s.http_status_4xx_15m : 0,
      http_status_5xx_15m: typeof s.http_status_5xx_15m === "number" ? s.http_status_5xx_15m : 0,
      http_bytes_in_15m: typeof s.http_bytes_in_15m === "number" ? s.http_bytes_in_15m : 0,
      http_bytes_out_15m: typeof s.http_bytes_out_15m === "number" ? s.http_bytes_out_15m : 0,
    }
  }
  return { instances, summary }
}

function parseUsageHistory(raw: unknown): UsageHistory {
  if (!raw || typeof raw !== "object") {
    return { window: "", resource: [], http: [] }
  }
  const o = raw as Record<string, unknown>
  return {
    window: typeof o.window === "string" ? o.window : "",
    resource: Array.isArray(o.resource) ? (o.resource as UsageHistory["resource"]) : [],
    http: Array.isArray(o.http) ? (o.http as UsageHistory["http"]) : [],
  }
}

export type APIMeta = {
  public_base_url: string
  public_base_url_configured: boolean
  webhook_path: string
  /** Hostname for the dashboard when split from API (e.g. app.example.com). */
  dashboard_public_host?: string
  /** Wildcard DNS base for PR previews, e.g. preview.example.com (DNS: *.preview.example.com → edge). */
  preview_base_domain?: string
  preview_retention_after_close_seconds?: number
  preview_idle_scale_seconds?: number
}

export type PreviewImmutableURL = {
  url: string
  deployment_id?: string
  github_commit?: string
}

export type PreviewLatestDeployment = {
  id: string
  github_commit: string
  phase: string
  build_status?: string
  created_at?: string
  preview_scaled_to_zero: boolean
  wake_requested_at?: string
}

export type PreviewEnvironment = {
  id: string
  pr_number: number
  head_branch: string
  head_sha: string
  lifecycle_state: "active" | "closed" | "cleanup_due"
  stable_url?: string
  closed_at?: string
  expires_at?: string
  latest_deployment?: PreviewLatestDeployment
  /** Per-deployment preview hostnames (unchanging until cleanup). */
  immutable_urls?: PreviewImmutableURL[]
}

export type GitHubSetup = {
  github_repository: string
  webhook_url: string
  webhook_path: string
  webhook_secret: string
  public_base_url_configured: boolean
  instructions: string
}

export type GitHead = {
  repository: string
  ref: string
  sha: string
  short_sha: string
  running_deployment_id: string
  running_commit: string
  latest_deployed_commit: string
  update_available: boolean
  github_token_configured: boolean
}

export type ProjectDomainDNSChallenge = {
  type: string
  name: string
  value: string
}

export type ProjectDomain = {
  id: string
  domain_name: string
  verified_at?: string
  deployment_id?: string
  dns_challenge?: ProjectDomainDNSChallenge
  instructions?: string
}

export const api = {
  authBootstrapStatus: () =>
    request<{ needs_bootstrap: boolean; bootstrap_token_configured: boolean }>(
      "/api/auth/bootstrap-status"
    ),
  authSession: () => request<AuthSession>("/api/auth/session"),
  authProviders: () => request<AuthPublicProvider[]>("/api/auth/providers"),
  authBootstrap: (data: {
    email: string
    password: string
    display_name?: string
    bootstrap_token?: string
  }) =>
    request<AuthSession>("/api/auth/bootstrap", {
      method: "POST",
      headers: data.bootstrap_token
        ? { "X-Kindling-Bootstrap-Token": data.bootstrap_token }
        : undefined,
      body: JSON.stringify({
        email: data.email,
        password: data.password,
        display_name: data.display_name,
      }),
    }),
  authLogin: (data: { email: string; password: string }) =>
    request<AuthSession>("/api/auth/login", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  authLogout: () =>
    request<{ ok: boolean }>("/api/auth/logout", {
      method: "POST",
      body: JSON.stringify({}),
    }),
  authSwitchOrg: (organization_id: string) =>
    request<AuthSession>("/api/auth/switch-org", {
      method: "POST",
      body: JSON.stringify({ organization_id }),
    }),
  listAdminAuthProviders: () =>
    request<AuthAdminProvider[]>("/api/auth/admin/providers"),
  updateAdminAuthProvider: (
    provider: "github" | "oidc",
    data: {
      display_name?: string
      enabled: boolean
      client_id?: string
      client_secret?: string
      clear_client_secret?: boolean
      issuer_url?: string
      scopes?: string
    }
  ) =>
    request<AuthAdminProvider>(`/api/auth/admin/providers/${provider}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  listAuthIdentities: () => request<AuthIdentity[]>("/api/auth/identities"),

  listOrgProviderConnections: () =>
    request<
      {
        id: string
        provider: string
        external_slug: string
        display_label: string
        has_credentials: boolean
        metadata: unknown
        created_at: string
        updated_at: string
      }[]
    >("/api/org/provider-connections"),
  createOrgProviderConnection: (data: {
    provider: "github" | "gitlab"
    external_slug: string
    display_label?: string
    token?: string
    metadata?: unknown
  }) =>
    request<unknown>("/api/org/provider-connections", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  deleteOrgProviderConnection: (id: string) =>
    request<void>(`/api/org/provider-connections/${id}`, { method: "DELETE" }),

  getMeta: () => request<APIMeta>("/api/meta"),
  updateMeta: (data: {
    public_base_url?: string
    dashboard_public_host?: string
    preview_base_domain?: string
    preview_retention_after_close_seconds?: number
    preview_idle_scale_seconds?: number
  }) =>
    request<APIMeta>("/api/meta", {
      method: "PUT",
      body: JSON.stringify(data),
    }),

  listProjects: () => request<Project[]>("/api/projects"),
  createProject: (data: {
    name: string
    github_repository?: string
    dockerfile_path?: string
    root_directory?: string
    desired_instance_count?: number
    build_only_on_root_changes?: boolean
  }) => request<Project>("/api/projects", { method: "POST", body: JSON.stringify(data) }),

  getProject: (id: string) => request<Project>(`/api/projects/${id}`),

  getProjectVolume: (id: string) => request<ProjectVolume>(`/api/projects/${id}/volume`),
  putProjectVolume: (id: string, data: { mount_path?: string; size_gb?: number }) =>
    request<ProjectVolume>(`/api/projects/${id}/volume`, { method: "PUT", body: JSON.stringify(data) }),
  deleteProjectVolume: (id: string) => request<void>(`/api/projects/${id}/volume`, { method: "DELETE" }),

  patchProject: (
    id: string,
    data: {
      desired_instance_count?: number
      scale_to_zero_enabled?: boolean
      build_only_on_root_changes?: boolean
    },
  ) =>
    request<Project>(`/api/projects/${id}`, { method: "PATCH", body: JSON.stringify(data) }),
  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: "DELETE" }),

  getGitHubSetup: (projectId: string) =>
    request<GitHubSetup>(`/api/projects/${projectId}/github-setup`),

  listProjectSecrets: (projectId: string) =>
    request<ProjectSecret[]>(`/api/projects/${projectId}/secrets`),

  upsertProjectSecret: (projectId: string, data: { name: string; value: string }) =>
    request<ProjectSecret>(`/api/projects/${projectId}/secrets`, {
      method: "POST",
      body: JSON.stringify(data),
    }),

  deleteProjectSecret: (projectId: string, secretId: string) =>
    request<void>(`/api/projects/${projectId}/secrets/${secretId}`, { method: "DELETE" }),

  rotateWebhookSecret: (projectId: string) =>
    request<{ github_webhook_secret: string; webhook_url: string }>(
      `/api/projects/${projectId}/rotate-webhook-secret`,
      { method: "POST", body: JSON.stringify({}) },
    ),

  getGitHead: (projectId: string, ref?: string) =>
    request<GitHead>(
      `/api/projects/${projectId}/git-head${ref != null && ref !== "" ? `?ref=${encodeURIComponent(ref)}` : ""}`,
    ),

  listDeployments: (projectId: string) =>
    request<Deployment[]>(`/api/projects/${projectId}/deployments`),

  listProjectPreviews: (projectId: string) =>
    request<PreviewEnvironment[]>(`/api/projects/${projectId}/previews`),

  redeployProjectPreview: (projectId: string, previewId: string) =>
    request<Deployment>(`/api/projects/${projectId}/previews/${previewId}/redeploy`, {
      method: "POST",
      body: JSON.stringify({}),
    }),

  deleteProjectPreview: (projectId: string, previewId: string) =>
    request<{ status: string }>(`/api/projects/${projectId}/previews/${previewId}`, {
      method: "DELETE",
    }),

  listProjectDomains: (projectId: string) => request<ProjectDomain[]>(`/api/projects/${projectId}/domains`),

  createProjectDomain: (projectId: string, domain_name: string) =>
    request<ProjectDomain>(`/api/projects/${projectId}/domains`, {
      method: "POST",
      body: JSON.stringify({ domain_name }),
    }),

  verifyProjectDomain: (projectId: string, domainId: string) =>
    request<ProjectDomain>(`/api/projects/${projectId}/domains/${domainId}/verify`, {
      method: "POST",
      body: JSON.stringify({}),
    }),

  deleteProjectDomain: (projectId: string, domainId: string) =>
    request<void>(`/api/projects/${projectId}/domains/${domainId}`, { method: "DELETE" }),

  listAllDeployments: (limit = 50) =>
    request<DeploymentListItem[]>(`/api/deployments?limit=${limit}`),

  getDeployment: (id: string) => request<Deployment>(`/api/deployments/${id}`),
  getDeploymentLogs: (id: string) => request<BuildLog[]>(`/api/deployments/${id}/logs`),
  getDeploymentInstanceMigration: (id: string) =>
    request<{ migration: DeploymentInstanceMigration | null }>(`/api/deployment-instances/${id}/migration`),
  liveMigrateDeploymentInstance: (id: string) =>
    request<DeploymentInstanceMigration>(`/api/deployment-instances/${id}/live-migrate`, {
      method: "POST",
      body: JSON.stringify({}),
    }),

  triggerDeploy: (projectId: string, commit: string) =>
    request<Deployment>(`/api/projects/${projectId}/deploy`, {
      method: "POST",
      body: JSON.stringify({ commit }),
    }),

  cancelDeployment: (id: string) =>
    request<void>(`/api/deployments/${id}/cancel`, { method: "POST" }),

  listServers: () => request<Server[]>("/api/servers"),

  getServerDetails: (id: string) => request<ServerDetail>(`/api/servers/${id}/details`),

  drainServer: (id: string) =>
    request<{ status: string }>(`/api/servers/${id}/drain`, { method: "POST", body: JSON.stringify({}) }),

  activateServer: (id: string) =>
    request<{ status: string }>(`/api/servers/${id}/activate`, { method: "POST", body: JSON.stringify({}) }),

  getProjectUsageCurrent: (projectId: string) =>
    request<unknown>(`/api/projects/${projectId}/usage/current`).then(parseUsageCurrent),

  getProjectUsageHistory: (projectId: string, window?: string) =>
    request<unknown>(
      `/api/projects/${projectId}/usage/history${window != null && window !== "" ? `?window=${encodeURIComponent(window)}` : ""}`,
    ).then(parseUsageHistory),
}

export function authProviderStartURL(
  provider: "github" | "oidc",
  mode: "login" | "link" = "login",
  returnTo?: string,
) {
  const path = mode === "link" ? `/api/auth/providers/${provider}/link` : `/api/auth/providers/${provider}/start`
  const url = new URL(`${API_BASE}${path}`)
  if (returnTo) url.searchParams.set("return_to", returnTo)
  return url.toString()
}

/** Topics for GET /api/events (must match server `internal/rpc` dashboard invalidation names). */
export const dashboardEventTopics = {
  projects: "projects",
  deployments: "deployments",
  servers: "servers",
  project: (id: string) => `project:${id}`,
  projectDeployments: (id: string) => `project_deployments:${id}`,
} as const

export type DashboardInvalidatePayload = {
  topic: string
  at?: string
}

/** Multiplexed dashboard SSE: refetch REST when you receive `invalidate` events. */
export function subscribeDashboardEvents(options: {
  topics: string[]
  onInvalidate?: (topic: string) => void
  onError?: (e: Event) => void
}): () => void {
  const topics = options.topics.filter((t) => t.trim() !== "")
  if (topics.length === 0) {
    return () => {}
  }
  const qs = new URLSearchParams()
  qs.set("topics", topics.join(","))
  const url = `${API_BASE}/api/events?${qs.toString()}`
  const es = new EventSource(url, { withCredentials: true })

  es.addEventListener("invalidate", (e) => {
    try {
      const d = JSON.parse((e as MessageEvent).data) as DashboardInvalidatePayload
      if (d.topic) options.onInvalidate?.(d.topic)
    } catch {
      /* ignore */
    }
  })

  es.onerror = (err) => {
    options.onError?.(err)
    // Let the browser reconnect (do not close).
  }

  return () => es.close()
}

/** SSE url for live deployment updates (use with EventSource). */
export function deploymentStreamURL(deploymentId: string): string {
  return `${API_BASE}/api/deployments/${deploymentId}/stream`
}

export function subscribeDeploymentStream(
  deploymentId: string,
  handlers: {
    onDeployment: (d: Deployment) => void
    onLogs: (logs: BuildLog[]) => void
    onDone?: () => void
    onError?: (e: Event) => void
  },
): () => void {
  const es = new EventSource(deploymentStreamURL(deploymentId), { withCredentials: true })

  es.addEventListener("deployment", (e) => {
    try {
      handlers.onDeployment(JSON.parse((e as MessageEvent).data) as Deployment)
    } catch {
      /* ignore */
    }
  })

  es.addEventListener("logs", (e) => {
    try {
      handlers.onLogs(JSON.parse((e as MessageEvent).data) as BuildLog[])
    } catch {
      /* ignore */
    }
  })

  es.addEventListener("done", () => {
    handlers.onDone?.()
    es.close()
  })

  es.onerror = (err) => {
    handlers.onError?.(err)
    es.close()
  }

  return () => es.close()
}
