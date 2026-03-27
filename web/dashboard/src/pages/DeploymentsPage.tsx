import { useCallback, useEffect, useMemo, useState } from "react"
import { Link } from "react-router-dom"
import {
  api,
  type DeploymentListItem,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
} from "@/lib/api"
import { Skeleton } from "@/components/ui/skeleton"
import {
  RocketIcon,
  ChevronRightIcon,
  GitBranchIcon,
  GitCommitHorizontalIcon,
} from "lucide-react"
import {
  phaseLabel,
  phaseDotColor,
  phaseToFilterGroup,
  type PhaseDotColor,
  type PhaseFilterGroup,
} from "@/lib/deploy-badge"
import { timeAgo } from "@/lib/time"
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
import { cn } from "@/lib/utils"

type EnvFilter = "all" | "production" | "preview"
type StatusFilter = "all" | PhaseFilterGroup

const ENV_OPTIONS: { value: EnvFilter; label: string }[] = [
  { value: "all", label: "All Environments" },
  { value: "production", label: "Production" },
  { value: "preview", label: "Preview" },
]

const STATUS_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "All Statuses" },
  { value: "ready", label: "Ready" },
  { value: "building", label: "Building" },
  { value: "error", label: "Error" },
  { value: "canceled", label: "Canceled" },
]

const DOT_CLASSES: Record<PhaseDotColor, string> = {
  green: "bg-emerald-500",
  amber: "bg-amber-500 animate-pulse",
  red: "bg-red-500",
  gray: "bg-zinc-400 dark:bg-zinc-500",
}

function StatusDot({ phase }: { phase: string }) {
  const color = phaseDotColor(phase)
  return (
    <span
      className={cn("inline-block size-2.5 rounded-full shrink-0", DOT_CLASSES[color])}
    />
  )
}

function EnvironmentBadge({ kind }: { kind: string | undefined }) {
  const isProduction = !kind || kind === "production"
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
        isProduction
          ? "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-400"
          : "border-zinc-200 bg-zinc-50 text-zinc-600 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-400",
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full",
          isProduction ? "bg-emerald-500" : "bg-zinc-400 dark:bg-zinc-500",
        )}
      />
      {isProduction ? "Production" : "Preview"}
    </span>
  )
}

function FilterSelect<T extends string>({
  value,
  onChange,
  options,
}: {
  value: T
  onChange: (v: T) => void
  options: { value: T; label: string }[]
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value as T)}
      className="h-8 rounded-md border border-input bg-background px-2.5 text-xs font-medium text-foreground shadow-sm transition-colors hover:bg-accent focus:outline-none focus:ring-1 focus:ring-ring"
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  )
}

export function DeploymentsPage() {
  const [items, setItems] = useState<DeploymentListItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [envFilter, setEnvFilter] = useState<EnvFilter>("all")
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")

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

  const filtered = useMemo(() => {
    let result = items
    if (envFilter !== "all") {
      result = result.filter((d) => {
        const kind = d.deployment_kind || "production"
        return kind === envFilter
      })
    }
    if (statusFilter !== "all") {
      result = result.filter((d) => phaseToFilterGroup(d.phase) === statusFilter)
    }
    return result
  }, [items, envFilter, statusFilter])

  if (loading) {
    return (
      <PageContainer size="wide">
        <div className="space-y-6">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  return (
    <PageContainer size="wide">
      <PageSection>
        {error && <PageErrorBanner message={error} />}

        <PageHeader>
          <div className="space-y-1">
            <PageTitle>Deployments</PageTitle>
            <PageDescription>Recent activity across all projects</PageDescription>
          </div>
        </PageHeader>

        <div className="flex items-center gap-2">
          <FilterSelect value={envFilter} onChange={setEnvFilter} options={ENV_OPTIONS} />
          <FilterSelect value={statusFilter} onChange={setStatusFilter} options={STATUS_OPTIONS} />
        </div>

        {filtered.length === 0 && items.length === 0 ? (
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
        ) : filtered.length === 0 ? (
          <Surface>
            <div className="flex flex-col items-center justify-center py-12 text-center px-4">
              <p className="text-sm text-muted-foreground">
                No deployments match the current filters.
              </p>
            </div>
          </Surface>
        ) : (
          <Surface>
            <ul className="divide-y">
              {filtered.map((d) => (
                <li key={d.id}>
                  <Link
                    to={`/deployments/${d.id}`}
                    className="group flex items-center gap-4 px-4 py-3 transition-colors hover:bg-accent/50"
                  >
                    {/* Project + Environment */}
                    <div className="flex items-center gap-3 min-w-0 w-52 shrink-0">
                      <div className="min-w-0">
                        <p className="text-sm font-medium truncate">{d.project_name}</p>
                        <div className="mt-1">
                          <EnvironmentBadge kind={d.deployment_kind} />
                        </div>
                      </div>
                    </div>

                    {/* Status + Age */}
                    <div className="w-44 shrink-0">
                      <div className="flex items-center gap-4">
                        <div className="flex items-center gap-1.5">
                          <StatusDot phase={d.phase} />
                          <span className="text-sm">{phaseLabel(d.phase)}</span>
                        </div>
                        <span className="text-xs text-muted-foreground">
                          {timeAgo(d.created_at)}
                        </span>
                      </div>
                      {d.blocked_reason ? (
                        <p className="mt-1 line-clamp-2 text-xs text-red-600 dark:text-red-400">
                          {d.blocked_reason}
                        </p>
                      ) : null}
                    </div>

                    {/* Branch + Commit */}
                    <div className="flex items-center gap-4 min-w-0 flex-1">
                      {d.github_branch && (
                        <span className="inline-flex items-center gap-1 text-xs text-muted-foreground truncate max-w-40">
                          <GitBranchIcon className="size-3 shrink-0" />
                          <span className="truncate">{d.github_branch}</span>
                        </span>
                      )}
                      {d.github_commit && (
                        <span className="inline-flex items-center gap-1 font-mono text-xs text-muted-foreground">
                          <GitCommitHorizontalIcon className="size-3 shrink-0" />
                          {d.github_commit.slice(0, 7)}
                        </span>
                      )}
                    </div>

                    {/* Date + Chevron */}
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="text-xs text-muted-foreground">
                        {d.created_at
                          ? new Date(d.created_at).toLocaleDateString(undefined, {
                              month: "short",
                              day: "numeric",
                            })
                          : ""}
                      </span>
                      <ChevronRightIcon className="size-4 text-muted-foreground/40 transition-transform group-hover:translate-x-0.5" />
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
