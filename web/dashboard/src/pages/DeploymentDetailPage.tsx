import { useEffect, useState, useRef, useCallback } from "react"
import { useParams, Link, useNavigate } from "react-router-dom"
import { api, type Deployment, type BuildLog, subscribeDeploymentStream, APIError } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Separator } from "@/components/ui/separator"
import { BuildLogLineBody } from "@/components/build-log-line-body"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { ArrowLeftIcon, ScrollTextIcon, LoaderIcon, XCircleIcon, RotateCwIcon, RadioIcon, InfoIcon } from "lucide-react"
import { isTerminalDeployment, phaseLabel, phaseVariant } from "@/lib/deploy-badge"

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
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [live, setLive] = useState(false)
  const logEndRef = useRef<HTMLDivElement>(null)
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

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logs])

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-32 rounded-xl" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    )
  }

  if (error || !deployment) {
    return (
      <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-6 text-destructive text-sm max-w-xl">
        {error || "Deployment not found"}
      </div>
    )
  }

  const terminal = isTerminalDeployment(deployment)

  return (
    <div className="space-y-6 max-w-5xl mx-auto w-full">
      <div>
        <Link
          to={`/projects/${deployment.project_id}`}
          className="text-sm text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
        >
          <ArrowLeftIcon className="size-3" /> Back to project
        </Link>
        <div className="flex flex-col gap-3 mt-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="flex flex-wrap items-center gap-2 min-w-0">
            <h1 className="text-2xl font-semibold tracking-tight">Deployment</h1>
            <Badge variant={phaseVariant(deployment.phase)}>{phaseLabel(deployment.phase)}</Badge>
            {!terminal && (
              <>
                {live ? (
                  <span className="inline-flex items-center gap-1 text-xs text-muted-foreground" title="Live updates">
                    <RadioIcon className="size-3 text-green-600 dark:text-green-400" />
                    Live
                  </span>
                ) : (
                  <LoaderIcon className="size-4 text-muted-foreground animate-spin" />
                )}
              </>
            )}
          </div>
        </div>
        <p className="text-sm text-muted-foreground mt-2">
          Commit and build logs are on the tabs below.
        </p>
      </div>

      <Tabs defaultValue="overview" className="min-w-0">
        <TabsList variant="line" className="w-full min-w-0 max-w-full justify-start overflow-x-auto">
          <TabsTrigger value="overview" className="shrink-0">
            <InfoIcon className="size-4" />
            Overview
          </TabsTrigger>
          <TabsTrigger value="logs" className="shrink-0">
            <ScrollTextIcon className="size-4" />
            Logs
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Status</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4 text-sm">
              <dl className="grid gap-3 sm:grid-cols-2">
                <div>
                  <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Commit</dt>
                  <dd className="font-mono text-sm mt-1 break-all">
                    {deployment.github_commit ? deployment.github_commit.slice(0, 8) : "manual"}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Created</dt>
                  <dd className="mt-1">
                    {deployment.created_at ? new Date(deployment.created_at).toLocaleString() : "—"}
                  </dd>
                </div>
                {(deployment.desired_instance_count != null || deployment.running_instance_count != null) && (
                  <div>
                    <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Instances</dt>
                    <dd className="font-mono text-sm mt-1">
                      {deployment.running_instance_count ?? 0} / {deployment.desired_instance_count ?? 1} running
                    </dd>
                  </div>
                )}
                {deployment.build_status && (
                  <div className="sm:col-span-2">
                    <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Build</dt>
                    <dd className="mt-1">{deployment.build_status}</dd>
                  </div>
                )}
              </dl>
              <Separator />
              <div className="space-y-2">
                <dt className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Reachability</dt>
                <DeploymentReachability reachable={deployment.reachable} />
              </div>
              <div className="flex flex-wrap gap-2 pt-2">
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
                    <XCircleIcon className="mr-2 size-4" />
                    Cancel
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
                    <RotateCwIcon className="mr-2 size-4" />
                    Redeploy
                  </Button>
                )}
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="logs" className="mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base flex items-center gap-2">
                <ScrollTextIcon className="size-4" />
                Build logs
              </CardTitle>
            </CardHeader>
            <CardContent>
              {logs.length === 0 ? (
                <div className="py-4 text-center">
                  {!terminal ? (
                    <div className="flex items-center justify-center gap-2 text-sm text-muted-foreground px-2">
                      <LoaderIcon className="size-4 animate-spin shrink-0" />
                      Waiting for build to start…
                    </div>
                  ) : (
                    <p className="text-sm text-muted-foreground">No build logs.</p>
                  )}
                </div>
              ) : (
                <div className="rounded-lg bg-muted/50 p-3 sm:p-4 font-mono text-xs leading-relaxed max-h-[min(70vh,720px)] overflow-y-auto space-y-0.5">
                  {logs.map((log) => (
                    <div key={logKey(log)} className={log.level === "error" ? "text-destructive" : "text-foreground"}>
                      <span className="text-muted-foreground mr-2">
                        {log.created_at ? new Date(log.created_at).toLocaleTimeString() : ""}
                      </span>
                      <BuildLogLineBody message={log.message} />
                    </div>
                  ))}
                  <div ref={logEndRef} />
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
