import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react"
import { useParams, useNavigate } from "react-router-dom"
import { api, type Deployment, type BuildLog, subscribeDeploymentStream, APIError } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Separator } from "@/components/ui/separator"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { ScrollTextIcon, LoaderIcon, XCircleIcon, RotateCwIcon, RadioIcon, InfoIcon } from "lucide-react"
import { isTerminalDeployment, phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import {
  PageContainer,
  PageHeader,
  PageTitle,
  PageBackLink,
  PageSection,
  MetadataGrid,
  MetadataItem,
  PageErrorBanner,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceBody,
} from "@/components/page-surface"

const DeploymentLogsPanel = lazy(() =>
  import("@/components/deployment-logs-panel").then((module) => ({
    default: module.DeploymentLogsPanel,
  }))
)

function logKey(l: BuildLog): string {
  if (l.id) return l.id
  return `${l.created_at}-${l.message.slice(0, 48)}`
}

function mergeLogs(prev: BuildLog[], chunk: BuildLog[]): BuildLog[] {
  const m = new Map<string, BuildLog>()
  for (const x of prev) {
    m.set(logKey(x), x)
  }
  for (const x of chunk) {
    m.set(logKey(x), x)
  }
  return [...m.values()].sort(
    (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
  )
}

export function DeploymentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [deployment, setDeployment] = useState<Deployment | null>(null)
  const [logs, setLogs] = useState<BuildLog[]>([])
  const [activeTab, setActiveTab] = useState<"overview" | "logs">("overview")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const deploymentRef = useRef<Deployment | null>(null)
  deploymentRef.current = deployment

  const stopPoll = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  useEffect(() => {
    if (!id) return
    let cancelled = false

    ;(async () => {
      setError(null)
      setLoading(true)
      stopPoll()
      setLive(false)
      try {
        const [d, l] = await Promise.all([api.getDeployment(id), api.getDeploymentLogs(id)])
        if (cancelled) return
        setDeployment(d)
        setLogs(l)
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof APIError ? e.message : String(e))
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
  }, [id, stopPoll])

  useEffect(() => {
    if (!id || loading) return
    const d = deploymentRef.current
    if (!d || isTerminalDeployment(d)) {
      setLive(false)
      stopPoll()
      return
    }

    stopPoll()
    setLive(true)

    const unsub = subscribeDeploymentStream(id, {
      onDeployment: (next) => setDeployment(next),
      onLogs: (chunk) => setLogs((prev) => mergeLogs(prev, chunk)),
      onDone: () => {
        setLive(false)
        stopPoll()
      },
      onError: () => {
        setLive(false)
        if (pollRef.current) return
        pollRef.current = setInterval(async () => {
          try {
            const [d2, l] = await Promise.all([api.getDeployment(id), api.getDeploymentLogs(id)])
            setDeployment(d2)
            setLogs(l)
            if (isTerminalDeployment(d2)) {
              stopPoll()
            }
          } catch {
            /* ignore */
          }
        }, 3000)
      },
    })

    return () => {
      unsub()
      stopPoll()
    }
  }, [id, loading, stopPoll])

  if (loading) {
    return (
      <PageContainer>
        <div className="space-y-4">
          <Skeleton className="h-5 w-32" />
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-32 rounded-xl" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  if (error || !deployment) {
    return (
      <PageContainer>
        <PageErrorBanner message={error || "Deployment not found"} className="max-w-xl" />
      </PageContainer>
    )
  }

  const terminal = isTerminalDeployment(deployment)

  return (
    <PageContainer>
      <PageSection>
        <div>
          <PageBackLink to={`/projects/${deployment.project_id}`}>Back to project</PageBackLink>
          <PageHeader>
            <div className="flex flex-wrap items-center gap-3 min-w-0">
              <PageTitle>Deployment</PageTitle>
              <Badge variant={phaseVariant(deployment.phase)}>{phaseLabel(deployment.phase)}</Badge>
              {!terminal && (
                <>
                  {live ? (
                    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground" title="Live updates">
                      <RadioIcon className="size-3 text-green-600 dark:text-green-400" /> Live
                    </span>
                  ) : (
                    <LoaderIcon className="size-4 text-muted-foreground animate-spin" />
                  )}
                </>
              )}
            </div>
          </PageHeader>
        </div>

        <Tabs
          value={activeTab}
          onValueChange={(value) => setActiveTab(value === "logs" ? "logs" : "overview")}
          className="min-w-0"
        >
          <TabsList variant="line" className="w-full min-w-0 max-w-full justify-start overflow-x-auto">
            <TabsTrigger value="overview" className="shrink-0">
              <InfoIcon className="size-4" /> Overview
            </TabsTrigger>
            <TabsTrigger value="logs" className="shrink-0">
              <ScrollTextIcon className="size-4" /> Logs
            </TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="mt-5">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Status</SurfaceTitle>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                <MetadataGrid>
                  <MetadataItem label="Commit">
                    <span className="font-mono text-sm break-all">
                      {deployment.github_commit ? deployment.github_commit.slice(0, 8) : "manual"}
                    </span>
                  </MetadataItem>
                  <MetadataItem label="Created">
                    {deployment.created_at ? new Date(deployment.created_at).toLocaleString() : "—"}
                  </MetadataItem>
                  {(deployment.desired_instance_count != null || deployment.running_instance_count != null) && (
                    <MetadataItem label="Instances">
                      <span className="font-mono text-sm">
                        {deployment.running_instance_count ?? 0} / {deployment.desired_instance_count ?? 1} running
                      </span>
                    </MetadataItem>
                  )}
                  {deployment.build_status && (
                    <MetadataItem label="Build" span="full">{deployment.build_status}</MetadataItem>
                  )}
                </MetadataGrid>
                <Separator />
                <div className="space-y-2">
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Reachability</p>
                  <DeploymentReachability reachable={deployment.reachable} />
                </div>
                <div className="flex flex-wrap gap-2 pt-1">
                  {!terminal && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={async () => {
                        if (!id) return
                        await api.cancelDeployment(id)
                        const d = await api.getDeployment(id)
                        setDeployment(d)
                      }}
                    >
                      <XCircleIcon className="mr-2 size-4" /> Cancel
                    </Button>
                  )}
                  {terminal && (
                    <Button
                      size="sm"
                      onClick={async () => {
                        const dep = await api.triggerDeploy(deployment.project_id, deployment.github_commit || "main")
                        navigate(`/deployments/${dep.id}`)
                      }}
                    >
                      <RotateCwIcon className="mr-2 size-4" /> Redeploy
                    </Button>
                  )}
                </div>
              </SurfaceBody>
            </Surface>
          </TabsContent>

          <TabsContent value="logs" className="mt-5">
            {activeTab === "logs" ? (
              <Suspense
                fallback={
                  <Surface>
                    <SurfaceHeader>
                      <div className="flex items-center gap-2">
                        <ScrollTextIcon className="size-4 text-muted-foreground" />
                        <SurfaceTitle>Build logs</SurfaceTitle>
                      </div>
                    </SurfaceHeader>
                    <SurfaceBody>
                      <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
                        <LoaderIcon className="size-4 animate-spin shrink-0" />
                        Loading logs…
                      </div>
                    </SurfaceBody>
                  </Surface>
                }
              >
                <DeploymentLogsPanel logs={logs} terminal={terminal} />
              </Suspense>
            ) : null}
          </TabsContent>
        </Tabs>
      </PageSection>
    </PageContainer>
  )
}
