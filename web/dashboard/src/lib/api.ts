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
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
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
  name: string
  github_repository: string
  dockerfile_path: string
  root_directory: string
  created_at: string
  updated_at: string
}

export type Deployment = {
  id: string
  project_id: string
  build_id?: string | null
  image_id?: string | null
  vm_id?: string | null
  github_commit: string
  running_at?: string | null
  stopped_at?: string | null
  failed_at?: string | null
  created_at?: string | null
  updated_at?: string | null
  build_status?: string
  phase: string
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
  status: string
  last_heartbeat_at: string
  created_at: string
}

export type APIMeta = {
  public_base_url: string
  public_base_url_configured: boolean
  webhook_path: string
}

export type GitHubSetup = {
  github_repository: string
  webhook_url: string
  webhook_path: string
  webhook_secret: string
  public_base_url_configured: boolean
  instructions: string
}

export const api = {
  getMeta: () => request<APIMeta>("/api/meta"),
  updateMeta: (data: { public_base_url: string }) =>
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
  }) => request<Project>("/api/projects", { method: "POST", body: JSON.stringify(data) }),

  getProject: (id: string) => request<Project>(`/api/projects/${id}`),
  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: "DELETE" }),

  getGitHubSetup: (projectId: string) =>
    request<GitHubSetup>(`/api/projects/${projectId}/github-setup`),

  rotateWebhookSecret: (projectId: string) =>
    request<{ github_webhook_secret: string; webhook_url: string }>(
      `/api/projects/${projectId}/rotate-webhook-secret`,
      { method: "POST", body: JSON.stringify({}) },
    ),

  listDeployments: (projectId: string) =>
    request<Deployment[]>(`/api/projects/${projectId}/deployments`),

  listAllDeployments: (limit = 50) =>
    request<DeploymentListItem[]>(`/api/deployments?limit=${limit}`),

  getDeployment: (id: string) => request<Deployment>(`/api/deployments/${id}`),
  getDeploymentLogs: (id: string) => request<BuildLog[]>(`/api/deployments/${id}/logs`),

  triggerDeploy: (projectId: string, commit: string) =>
    request<Deployment>(`/api/projects/${projectId}/deploy`, {
      method: "POST",
      body: JSON.stringify({ commit }),
    }),

  cancelDeployment: (id: string) =>
    request<void>(`/api/deployments/${id}/cancel`, { method: "POST" }),

  listServers: () => request<Server[]>("/api/servers"),
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
  const es = new EventSource(deploymentStreamURL(deploymentId))

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
