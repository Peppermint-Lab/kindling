import { useEffect, useState } from "react"
import { useParams, Link } from "react-router-dom"
import { api, type Deployment, type BuildLog } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { ArrowLeftIcon, ScrollTextIcon } from "lucide-react"

function deploymentStatus(dep: Deployment): { label: string; variant: "default" | "secondary" | "destructive" | "outline" } {
  if (dep.failed_at) return { label: "Failed", variant: "destructive" }
  if (dep.stopped_at) return { label: "Stopped", variant: "secondary" }
  if (dep.running_at) return { label: "Running", variant: "default" }
  return { label: "Pending", variant: "outline" }
}

export function DeploymentDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [deployment, setDeployment] = useState<Deployment | null>(null)
  const [logs, setLogs] = useState<BuildLog[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!id) return
    Promise.all([api.getDeployment(id), api.getDeploymentLogs(id)])
      .then(([d, l]) => { setDeployment(d); setLogs(l) })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

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
      <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-6 text-destructive text-sm">
        {error || "Deployment not found"}
      </div>
    )
  }

  const status = deploymentStatus(deployment)

  return (
    <div className="space-y-6">
      <div>
        <Link to={`/projects/${deployment.project_id}`} className="text-sm text-muted-foreground hover:text-foreground inline-flex items-center gap-1">
          <ArrowLeftIcon className="size-3" /> Back to project
        </Link>
        <div className="flex items-center gap-3 mt-2">
          <h1 className="text-2xl font-semibold tracking-tight">Deployment</h1>
          <Badge variant={status.variant}>{status.label}</Badge>
        </div>
        <p className="text-sm text-muted-foreground mt-1 font-mono">
          {deployment.github_commit ? deployment.github_commit.slice(0, 8) : "manual"}
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <ScrollTextIcon className="size-4" />
            Build Logs
          </CardTitle>
        </CardHeader>
        <CardContent>
          {logs.length === 0 ? (
            <p className="text-sm text-muted-foreground py-4 text-center">No build logs yet.</p>
          ) : (
            <div className="rounded-lg bg-muted/50 p-4 font-mono text-xs leading-relaxed max-h-[600px] overflow-y-auto space-y-0.5">
              {logs.map((log, i) => (
                <div key={i} className={log.level === "error" ? "text-destructive" : "text-foreground"}>
                  <span className="text-muted-foreground mr-2">
                    {new Date(log.created_at).toLocaleTimeString()}
                  </span>
                  {log.message}
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
