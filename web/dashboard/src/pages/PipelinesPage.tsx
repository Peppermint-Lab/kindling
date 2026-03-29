import { useCallback, useEffect, useMemo, useState } from "react"
import { Link } from "react-router-dom"
import { api, type CIJob, APIError } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Badge } from "@/components/ui/badge"
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
import { ChevronRightIcon, FolderGitIcon, GitBranchIcon } from "lucide-react"
import { timeAgo } from "@/lib/time"

type StatusFilter = "all" | "queued" | "running" | "successful" | "failed" | "canceled"
type SourceFilter = "all" | "local_workflow_run" | "github_actions_runner"

const STATUS_OPTIONS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "All Statuses" },
  { value: "queued", label: "Queued" },
  { value: "running", label: "Running" },
  { value: "successful", label: "Successful" },
  { value: "failed", label: "Failed" },
  { value: "canceled", label: "Canceled" },
]

const SOURCE_OPTIONS: { value: SourceFilter; label: string }[] = [
  { value: "all", label: "All Sources" },
  { value: "local_workflow_run", label: "Local CLI" },
  { value: "github_actions_runner", label: "GitHub Actions" },
]

function ciStatusVariant(status: CIJob["status"]): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "successful":
      return "default"
    case "failed":
      return "destructive"
    case "canceled":
      return "outline"
    default:
      return "secondary"
  }
}

function ciStatusLabel(status: CIJob["status"]): string {
  switch (status) {
    case "queued":
      return "Queued"
    case "running":
      return "Running"
    case "successful":
      return "Successful"
    case "failed":
      return "Failed"
    case "canceled":
      return "Canceled"
    default:
      return status
  }
}

function sourceLabel(source: CIJob["source"]): string {
  if (source === "local_workflow_run") return "Local CLI"
  if (source === "github_actions_runner") return "GitHub Actions"
  return source
}

function isTerminalCIJob(status: CIJob["status"]): boolean {
  return status === "successful" || status === "failed" || status === "canceled"
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

export function PipelinesPage() {
  const [items, setItems] = useState<CIJob[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [cancelingId, setCancelingId] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("all")

  const loadJobs = useCallback(async (opts?: { initial?: boolean }) => {
    const initial = opts?.initial ?? false
    if (initial) {
      setLoading(true)
      setError(null)
    }
    try {
      const nextJobs = await api.listAllCIJobs(100)
      setItems(nextJobs)
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      if (initial) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadJobs({ initial: true })
  }, [loadJobs])

  useEffect(() => {
    const interval = window.setInterval(() => {
      void loadJobs()
    }, 4000)
    return () => window.clearInterval(interval)
  }, [loadJobs])

  const handleCancel = async (jobId: string) => {
    setCancelingId(jobId)
    try {
      await api.cancelCIJob(jobId)
      await loadJobs()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not cancel pipeline")
    } finally {
      setCancelingId(null)
    }
  }

  const filtered = useMemo(() => {
    return items.filter((job) => {
      if (statusFilter !== "all" && job.status !== statusFilter) return false
      if (sourceFilter !== "all" && job.source !== sourceFilter) return false
      return true
    })
  }, [items, sourceFilter, statusFilter])

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
        {error ? <PageErrorBanner message={error} /> : null}

        <PageHeader>
          <div className="space-y-1">
            <PageTitle>Pipelines</PageTitle>
            <PageDescription>Recent CI activity across all projects in this organization.</PageDescription>
          </div>
        </PageHeader>

        <div className="flex items-center gap-2">
          <FilterSelect value={statusFilter} onChange={setStatusFilter} options={STATUS_OPTIONS} />
          <FilterSelect value={sourceFilter} onChange={setSourceFilter} options={SOURCE_OPTIONS} />
        </div>

        {filtered.length === 0 && items.length === 0 ? (
          <Surface>
            <EmptyState
              icon={<GitBranchIcon className="size-10" />}
              title="No pipelines yet"
              description="Run a workflow through Kindling CI and it will show up here."
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
              <p className="text-sm text-muted-foreground">No pipelines match the current filters.</p>
            </div>
          </Surface>
        ) : (
          <Surface>
            <ul className="divide-y">
              {filtered.map((job) => (
                <li key={job.id}>
                  <div className="flex items-center gap-2 pr-3">
                    <Link
                      to={`/ci/jobs/${job.id}`}
                      className="group flex min-w-0 flex-1 items-center gap-4 px-4 py-3 transition-colors hover:bg-accent/50"
                    >
                      <div className="min-w-0 w-56 shrink-0">
                        <p className="text-sm font-medium truncate">{job.project_name || "Unknown project"}</p>
                        <p className="mt-1 text-xs text-muted-foreground">{sourceLabel(job.source)}</p>
                      </div>

                      <div className="min-w-0 flex-1">
                        <div className="flex min-w-0 flex-wrap items-center gap-2">
                          <Badge variant={ciStatusVariant(job.status)}>{ciStatusLabel(job.status)}</Badge>
                          <span className="truncate text-sm font-medium">{job.workflow_name || "Workflow"}</span>
                          {job.selected_job_id ? (
                            <span className="font-mono text-xs text-muted-foreground">{job.selected_job_id}</span>
                          ) : null}
                        </div>
                        <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                          <span>{job.execution_backend || "Pending backend"}</span>
                          <span>•</span>
                          <span>{job.require_microvm ? "MicroVM required" : "Host fallback allowed"}</span>
                          {job.exit_code != null ? (
                            <>
                              <span>•</span>
                              <span>Exit {job.exit_code}</span>
                            </>
                          ) : null}
                        </div>
                      </div>

                      <div className="hidden w-28 shrink-0 text-xs text-muted-foreground sm:block">
                        {job.created_at ? timeAgo(job.created_at) : "just now"}
                      </div>

                      <div className="hidden min-w-0 w-40 shrink-0 text-xs text-muted-foreground md:block">
                        <div className="inline-flex items-center gap-1.5">
                          <FolderGitIcon className="size-3.5" />
                          <span className="truncate">{job.workflow_file || "workflow file"}</span>
                        </div>
                      </div>

                      <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground/40 transition-transform group-hover:translate-x-0.5" />
                    </Link>

                    {!isTerminalCIJob(job.status) ? (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={cancelingId === job.id}
                        onClick={() => void handleCancel(job.id)}
                      >
                        {cancelingId === job.id ? "Canceling…" : "Cancel"}
                      </Button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
          </Surface>
        )}
      </PageSection>
    </PageContainer>
  )
}
