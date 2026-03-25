import { useEffect, useState } from "react"
import { useParams, Link, useNavigate } from "react-router-dom"
import { api, type Project, type Deployment } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
import { ArrowLeftIcon, RocketIcon, TrashIcon } from "lucide-react"

function deploymentStatus(dep: Deployment): { label: string; variant: "default" | "secondary" | "destructive" | "outline" } {
  if (dep.failed_at) return { label: "Failed", variant: "destructive" }
  if (dep.stopped_at) return { label: "Stopped", variant: "secondary" }
  if (dep.running_at) return { label: "Running", variant: "default" }
  return { label: "Pending", variant: "outline" }
}

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [project, setProject] = useState<Project | null>(null)
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [deployDialogOpen, setDeployDialogOpen] = useState(false)
  const [deploying, setDeploying] = useState(false)
  const [commitSha, setCommitSha] = useState("")

  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)

  useEffect(() => {
    if (!id) return
    Promise.all([api.getProject(id), api.listDeployments(id)])
      .then(([p, d]) => { setProject(p); setDeployments(d) })
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }, [id])

  const handleDeploy = async () => {
    if (!id || !commitSha.trim()) return
    setDeploying(true)
    try {
      const dep = await api.triggerDeploy(id, commitSha.trim())
      setDeployDialogOpen(false)
      setCommitSha("")
      navigate(`/deployments/${dep.id}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Deploy failed")
    } finally {
      setDeploying(false)
    }
  }

  const handleDelete = async () => {
    if (!id) return
    setDeleting(true)
    try {
      await api.deleteProject(id)
      navigate("/projects")
    } catch (e) {
      setError(e instanceof Error ? e.message : "Delete failed")
    } finally {
      setDeleting(false)
    }
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-32 rounded-xl" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }

  if (error && !project) {
    return (
      <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-6 text-destructive text-sm">
        {error}
      </div>
    )
  }

  if (!project) return null

  return (
    <div className="space-y-6">
      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <div>
        <Link to="/projects" className="text-sm text-muted-foreground hover:text-foreground inline-flex items-center gap-1">
          <ArrowLeftIcon className="size-3" /> Projects
        </Link>
        <div className="flex items-center justify-between mt-2">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">{project.name}</h1>
            {project.github_repository && (
              <p className="text-sm text-muted-foreground mt-1 font-mono">{project.github_repository}</p>
            )}
          </div>
          <div className="flex gap-2">
            <Button size="sm" onClick={() => setDeployDialogOpen(true)}>
              <RocketIcon className="mr-2 size-4" />
              Deploy
            </Button>
            <Button size="sm" variant="outline" onClick={() => setDeleteDialogOpen(true)}>
              <TrashIcon className="size-4" />
            </Button>
          </div>
        </div>
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
              <Button size="sm" className="mt-3" onClick={() => setDeployDialogOpen(true)}>
                Deploy now
              </Button>
            </div>
          ) : (
            <div className="space-y-3">
              {deployments.map((dep) => {
                const status = deploymentStatus(dep)
                return (
                  <Link key={dep.id} to={`/deployments/${dep.id}`}>
                    <div className="flex items-center justify-between rounded-lg border p-3 hover:bg-accent/50 transition-colors cursor-pointer">
                      <div className="flex items-center gap-3">
                        <Badge variant={status.variant}>{status.label}</Badge>
                        <span className="font-mono text-sm">
                          {dep.github_commit ? dep.github_commit.slice(0, 8) : "manual"}
                        </span>
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {new Date(dep.created_at).toLocaleString()}
                      </span>
                    </div>
                  </Link>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>

      {/* Deploy Dialog */}
      <Dialog open={deployDialogOpen} onOpenChange={setDeployDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deploy</DialogTitle>
            <DialogDescription>Trigger a new deployment for {project.name}.</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="commit">Commit SHA</Label>
            <Input
              id="commit"
              placeholder="abc123..."
              value={commitSha}
              onChange={(e) => setCommitSha(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleDeploy()}
              className="font-mono"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeployDialogOpen(false)}>Cancel</Button>
            <Button onClick={handleDeploy} disabled={!commitSha.trim() || deploying}>
              {deploying ? "Deploying..." : "Deploy"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Dialog */}
      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Project</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete <strong>{project.name}</strong>? This will remove all deployments and cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialogOpen(false)}>Cancel</Button>
            <Button variant="destructive" onClick={handleDelete} disabled={deleting}>
              {deleting ? "Deleting..." : "Delete Project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
