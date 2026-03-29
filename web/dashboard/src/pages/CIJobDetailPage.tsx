import { useCallback, useEffect, useRef, useState } from "react"
import { useParams } from "react-router-dom"
import {
  api,
  type CIJob,
  type CIJobArtifact,
  type CIJobLog,
  APIError,
} from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  PageBackLink,
  PageContainer,
  PageErrorBanner,
  PageHeader,
  PageSection,
  PageTitle,
  MetadataGrid,
  MetadataItem,
  EmptyState,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceBody,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceDescription,
} from "@/components/page-surface"
import { FolderIcon, InfoIcon, ScrollTextIcon } from "lucide-react"

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

function isTerminalCIJob(status: CIJob["status"]): boolean {
  return status === "successful" || status === "failed" || status === "canceled"
}

export function CIJobDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [job, setJob] = useState<CIJob | null>(null)
  const [logs, setLogs] = useState<CIJobLog[]>([])
  const [artifacts, setArtifacts] = useState<CIJobArtifact[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [canceling, setCanceling] = useState(false)
  const [activeTab, setActiveTab] = useState<"overview" | "logs" | "artifacts">("overview")
  const jobRef = useRef<CIJob | null>(null)
  jobRef.current = job

  const loadJob = useCallback(async (opts?: { initial?: boolean }) => {
    if (!id) return
    const initial = opts?.initial ?? false
    if (initial) {
      setLoading(true)
      setError(null)
    }
    try {
      const [nextJob, nextLogs, nextArtifacts] = await Promise.all([
        api.getCIJob(id),
        api.getCIJobLogs(id),
        api.getCIJobArtifacts(id),
      ])
      setJob(nextJob)
      setLogs(nextLogs)
      setArtifacts(nextArtifacts)
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      if (initial) setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void loadJob({ initial: true })
  }, [loadJob])

  useEffect(() => {
    if (!id) return
    const interval = window.setInterval(() => {
      if (!jobRef.current || !isTerminalCIJob(jobRef.current.status)) {
        void loadJob()
      }
    }, 3000)
    return () => window.clearInterval(interval)
  }, [id, loadJob])

  const handleCancel = async () => {
    if (!id) return
    setCanceling(true)
    setActionError(null)
    try {
      await api.cancelCIJob(id)
      await loadJob()
    } catch (e) {
      setActionError(e instanceof APIError ? e.message : "Could not cancel CI job")
    } finally {
      setCanceling(false)
    }
  }

  if (loading) {
    return (
      <PageContainer>
        <div className="space-y-4">
          <Skeleton className="h-5 w-28" />
          <Skeleton className="h-8 w-40" />
          <Skeleton className="h-32 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  if (error || !job) {
    return (
      <PageContainer>
        <PageErrorBanner message={error || "CI job not found"} className="max-w-xl" />
      </PageContainer>
    )
  }

  return (
    <PageContainer>
      <PageSection>
        <div>
          <PageBackLink to={`/projects/${job.project_id}`}>Back to project</PageBackLink>
          <PageHeader>
            <div className="flex flex-wrap items-center gap-3 min-w-0">
              <PageTitle>CI job</PageTitle>
              <Badge variant={ciStatusVariant(job.status)}>{ciStatusLabel(job.status)}</Badge>
            </div>
            {!isTerminalCIJob(job.status) ? (
              <Button size="sm" variant="outline" disabled={canceling} onClick={() => void handleCancel()}>
                {canceling ? "Canceling…" : "Cancel"}
              </Button>
            ) : null}
          </PageHeader>
          {actionError ? <PageErrorBanner message={actionError} /> : null}
        </div>

        <Tabs
          value={activeTab}
          onValueChange={(value) =>
            setActiveTab(value === "logs" || value === "artifacts" ? value : "overview")
          }
          className="min-w-0"
        >
          <TabsList variant="line" className="w-full min-w-0 max-w-full justify-start overflow-x-auto">
            <TabsTrigger value="overview" className="shrink-0">
              <InfoIcon className="size-4" /> Overview
            </TabsTrigger>
            <TabsTrigger value="logs" className="shrink-0">
              <ScrollTextIcon className="size-4" /> Logs
            </TabsTrigger>
            <TabsTrigger value="artifacts" className="shrink-0">
              <FolderIcon className="size-4" /> Artifacts
            </TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="mt-5">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Execution</SurfaceTitle>
                <SurfaceDescription>Workflow, runtime backend, and final outcome for this job.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                {job.error_message ? <PageErrorBanner message={job.error_message} /> : null}
                <MetadataGrid>
                  <MetadataItem label="Workflow">{job.workflow_name || "—"}</MetadataItem>
                  <MetadataItem label="Job">{job.selected_job_id || "Entire workflow"}</MetadataItem>
                  <MetadataItem label="Source">{job.source}</MetadataItem>
                  <MetadataItem label="Backend">{job.execution_backend || "Pending selection"}</MetadataItem>
                  <MetadataItem label="MicroVM">{job.require_microvm ? "Required" : "Optional"}</MetadataItem>
                  <MetadataItem label="Exit code">
                    {job.exit_code != null ? <span className="font-mono">{job.exit_code}</span> : "—"}
                  </MetadataItem>
                  <MetadataItem label="Event">{job.event_name || "—"}</MetadataItem>
                  <MetadataItem label="Created">
                    {job.created_at ? new Date(job.created_at).toLocaleString() : "—"}
                  </MetadataItem>
                  <MetadataItem label="Started">
                    {job.started_at ? new Date(job.started_at).toLocaleString() : "—"}
                  </MetadataItem>
                  <MetadataItem label="Finished">
                    {job.finished_at ? new Date(job.finished_at).toLocaleString() : "—"}
                  </MetadataItem>
                  <MetadataItem label="Workflow file" span="full">
                    <span className="font-mono text-xs break-all">{job.workflow_file || "—"}</span>
                  </MetadataItem>
                  <MetadataItem label="Inputs" span="full">
                    {job.inputs && Object.keys(job.inputs).length > 0 ? (
                      <div className="space-y-2">
                        {Object.entries(job.inputs).map(([key, value]) => (
                          <div key={key} className="flex flex-wrap items-center gap-2">
                            <span className="font-mono text-xs rounded bg-muted px-2 py-1">{key}</span>
                            <span className="font-mono text-xs break-all">{value}</span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      "No inputs"
                    )}
                  </MetadataItem>
                </MetadataGrid>
              </SurfaceBody>
            </Surface>
          </TabsContent>

          <TabsContent value="logs" className="mt-5">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Logs</SurfaceTitle>
                <SurfaceDescription>Captured line-by-line job output.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody>
                {logs.length === 0 ? (
                  <EmptyState title="No logs yet" description="Logs will appear here once the job starts emitting output." className="py-12" />
                ) : (
                  <div className="rounded-lg border bg-muted/20">
                    <div className="max-h-[32rem] overflow-auto p-4 font-mono text-xs leading-6">
                      {logs.map((line) => (
                        <div key={line.id} className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 py-0.5">
                          <span className="text-muted-foreground whitespace-nowrap">
                            {new Date(line.created_at).toLocaleTimeString()}
                          </span>
                          <span className="break-words">{line.message}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>

          <TabsContent value="artifacts" className="mt-5">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Artifacts</SurfaceTitle>
                <SurfaceDescription>Files registered for this job.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody>
                {artifacts.length === 0 ? (
                  <EmptyState title="No artifacts" description="This job did not publish any artifacts." className="py-12" />
                ) : (
                  <ul className="divide-y rounded-lg border">
                    {artifacts.map((artifact) => (
                      <li key={artifact.id} className="flex flex-col gap-1 px-4 py-3">
                        <p className="font-medium">{artifact.name}</p>
                        <p className="font-mono text-xs break-all text-muted-foreground">{artifact.path}</p>
                        {artifact.created_at ? (
                          <p className="text-xs text-muted-foreground">
                            {new Date(artifact.created_at).toLocaleString()}
                          </p>
                        ) : null}
                      </li>
                    ))}
                  </ul>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>
        </Tabs>
      </PageSection>
    </PageContainer>
  )
}
