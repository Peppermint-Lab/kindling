import { useEffect, useState } from "react"
import { useParams, Link } from "react-router-dom"
import { api, type Project, type Deployment, uuidToString } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { ArrowLeftIcon, RocketIcon } from "lucide-react"

function deploymentStatus(dep: Deployment): { label: string; variant: "default" | "secondary" | "destructive" | "outline" } {
  if (dep.failed_at?.Valid) return { label: "Failed", variant: "destructive" }
  if (dep.stopped_at?.Valid) return { label: "Stopped", variant: "secondary" }
  if (dep.running_at?.Valid) return { label: "Running", variant: "default" }
  return { label: "Pending", variant: "outline" }
}

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [project, setProject] = useState<Project | null>(null)
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!id) return
    Promise.all([api.getProject(id), api.listDeployments(id)])
      .then(([p, d]) => { setProject(p); setDeployments(d) })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-32 rounded-xl" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }

  if (error || !project) {
    return (
      <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-6 text-destructive text-sm">
        {error || "Project not found"}
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <Link to="/projects" className="text-sm text-muted-foreground hover:text-foreground inline-flex items-center gap-1">
          <ArrowLeftIcon className="size-3" /> Projects
        </Link>
        <h1 className="text-2xl font-semibold tracking-tight mt-2">{project.name}</h1>
        {project.github_repository && (
          <p className="text-sm text-muted-foreground mt-1 font-mono">{project.github_repository}</p>
        )}
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Deployments</CardTitle>
        </CardHeader>
        <CardContent>
          {deployments.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-8 text-center">
              <RocketIcon className="size-8 text-muted-foreground mb-3" />
              <p className="text-sm text-muted-foreground">No deployments yet.</p>
            </div>
          ) : (
            <div className="space-y-3">
              {deployments.map((dep) => {
                const depId = uuidToString(dep.id)
                const status = deploymentStatus(dep)
                return (
                  <Link key={depId} to={`/deployments/${depId}`}>
                    <div className="flex items-center justify-between rounded-lg border p-3 hover:bg-accent/50 transition-colors cursor-pointer">
                      <div className="flex items-center gap-3">
                        <Badge variant={status.variant}>{status.label}</Badge>
                        <span className="font-mono text-sm">
                          {dep.github_commit ? dep.github_commit.slice(0, 8) : "manual"}
                        </span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {dep.created_at?.Valid
                          ? new Date(dep.created_at.Time).toLocaleString()
                          : ""}
                      </span>
                    </div>
                  </Link>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
