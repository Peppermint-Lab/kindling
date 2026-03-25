const API_BASE = import.meta.env.VITE_API_URL || "http://localhost:8080"

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`API ${res.status}: ${body}`)
  }
  if (res.status === 204) return undefined as T
  return res.json()
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
  github_commit: string
  running_at: string | null
  stopped_at: string | null
  failed_at: string | null
  created_at: string
}

export type BuildLog = {
  id: string
  message: string
  level: string
  created_at: string
}

export const api = {
  listProjects: () => request<Project[]>("/api/projects"),
  createProject: (data: { name: string; github_repository?: string }) =>
    request<Project>("/api/projects", { method: "POST", body: JSON.stringify(data) }),
  getProject: (id: string) => request<Project>(`/api/projects/${id}`),
  deleteProject: (id: string) => request<void>(`/api/projects/${id}`, { method: "DELETE" }),
  listDeployments: (projectId: string) => request<Deployment[]>(`/api/projects/${projectId}/deployments`),
  getDeployment: (id: string) => request<Deployment>(`/api/deployments/${id}`),
  getDeploymentLogs: (id: string) => request<BuildLog[]>(`/api/deployments/${id}/logs`),
}
