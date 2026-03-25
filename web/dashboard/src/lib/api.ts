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
  id: { Bytes: string; Valid: boolean }
  name: string
  github_repository: string
  dockerfile_path: string
  root_directory: string
  created_at: { Time: string; Valid: boolean }
}

export type Deployment = {
  id: { Bytes: string; Valid: boolean }
  project_id: { Bytes: string; Valid: boolean }
  github_commit: string
  running_at: { Time: string; Valid: boolean }
  stopped_at: { Time: string; Valid: boolean }
  failed_at: { Time: string; Valid: boolean }
  created_at: { Time: string; Valid: boolean }
}

export type BuildLog = {
  id: { Bytes: string; Valid: boolean }
  message: string
  level: string
  created_at: { Time: string; Valid: boolean }
}

export function uuidToString(id: { Bytes: string }): string {
  // pgtype.UUID serializes Bytes as base64 — decode and format
  const bytes = atob(id.Bytes)
  const hex = Array.from(bytes, (c) => c.charCodeAt(0).toString(16).padStart(2, "0")).join("")
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
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
