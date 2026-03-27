import { useCallback, useEffect, useState } from "react"
import { Link } from "react-router-dom"
import {
  api,
  type DeploymentListItem,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
} from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { RocketIcon } from "lucide-react"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"

export function DeploymentsPage() {
  const [items, setItems] = useState<DeploymentListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const loadDeployments = useCallback((opts?: { initial?: boolean }) => {
    const initial = opts?.initial ?? false
    if (initial) setLoading(true)
    return api
      .listAllDeployments(100)
      .then(setItems)
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => {
        if (initial) setLoading(false)
      })
  }, [])

  useEffect(() => {
    const id = window.setTimeout(() => {
      void loadDeployments({ initial: true })
    }, 0)
    return () => clearTimeout(id)
  }, [loadDeployments])

  useEffect(() => {
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadDeployments()
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.deployments],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [loadDeployments])

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-6 max-w-5xl mx-auto w-full">
      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <div className="flex flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Deployments</h1>
        <p className="text-sm text-muted-foreground">Recent activity across all projects</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <RocketIcon className="size-4" />
            Recent
          </CardTitle>
        </CardHeader>
        <CardContent>
          {items.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-12 text-center px-2">
              <RocketIcon className="size-10 text-muted-foreground mb-4" />
              <p className="text-muted-foreground">No deployments yet.</p>
              <p className="text-sm text-muted-foreground mt-1 max-w-sm">
                Create a project and deploy, or connect a GitHub webhook to deploy on push.
              </p>
              <Link
                to="/projects"
                className="mt-4 text-sm font-medium text-primary underline-offset-4 hover:underline"
              >
                Go to projects
              </Link>
            </div>
          ) : (
            <ul className="divide-y rounded-lg border">
              {items.map((d) => (
                <li key={d.id}>
                  <Link
                    to={`/deployments/${d.id}`}
                    className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between hover:bg-accent/50 transition-colors"
                  >
                    <div className="flex flex-wrap items-center gap-2 min-w-0">
                      <Badge variant={phaseVariant(d.phase)}>{phaseLabel(d.phase)}</Badge>
                      <span className="font-medium truncate">{d.project_name}</span>
                      <span className="font-mono text-xs text-muted-foreground truncate">
                        {d.github_commit ? d.github_commit.slice(0, 8) : "—"}
                      </span>
                    </div>
                    <span className="text-xs text-muted-foreground shrink-0">
                      {d.created_at
                        ? new Date(d.created_at).toLocaleString()
                        : ""}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
