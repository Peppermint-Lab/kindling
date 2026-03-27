import { useCallback, useEffect, useState } from "react"
import { Link } from "react-router-dom"
import {
  api,
  type DeploymentListItem,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
} from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { RocketIcon, ChevronRightIcon } from "lucide-react"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import {
  PageContainer,
  PageHeader,
  PageTitle,
  PageDescription,
  PageSection,
  EmptyState,
  PageErrorBanner,
} from "@/components/page-layout"
import { Surface } from "@/components/page-surface"

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
      <PageContainer>
        <div className="space-y-6">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  return (
    <PageContainer>
      <PageSection>
        {error && <PageErrorBanner message={error} />}

        <PageHeader>
          <div className="space-y-1">
            <PageTitle>Deployments</PageTitle>
            <PageDescription>Recent activity across all projects</PageDescription>
          </div>
        </PageHeader>

        {items.length === 0 ? (
          <Surface>
            <EmptyState
              icon={<RocketIcon className="size-10" />}
              title="No deployments yet"
              description="Create a project and deploy, or connect a GitHub webhook to deploy on push."
              action={
                <Link
                  to="/projects"
                  className="text-sm font-medium text-primary underline-offset-4 hover:underline"
                >
                  Go to projects
                </Link>
              }
            />
          </Surface>
        ) : (
          <Surface>
            <ul className="divide-y">
              {items.map((d) => (
                <li key={d.id}>
                  <Link
                    to={`/deployments/${d.id}`}
                    className="list-row group"
                  >
                    <div className="flex flex-wrap items-center gap-2 min-w-0">
                      <Badge variant={phaseVariant(d.phase)}>{phaseLabel(d.phase)}</Badge>
                      <span className="font-medium text-sm truncate">{d.project_name}</span>
                      <span className="font-mono text-xs text-muted-foreground truncate">
                        {d.github_commit ? d.github_commit.slice(0, 8) : "—"}
                      </span>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-muted-foreground shrink-0">
                        {d.created_at
                          ? new Date(d.created_at).toLocaleString()
                          : ""}
                      </span>
                      <ChevronRightIcon className="size-4 text-muted-foreground/40 shrink-0 transition-transform group-hover:translate-x-0.5" />
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          </Surface>
        )}
      </PageSection>
    </PageContainer>
  )
}
