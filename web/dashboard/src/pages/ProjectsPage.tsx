import { useCallback, useEffect, useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { api, type Project, APIError, dashboardEventTopics, subscribeDashboardEvents } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog"
import { FolderIcon, PlusIcon } from "lucide-react"

export function ProjectsPage() {
  const navigate = useNavigate()
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [form, setForm] = useState({
    name: "",
    github_repository: "",
    dockerfile_path: "Dockerfile",
    root_directory: "/",
    desired_instance_count: 1,
  })

  const loadProjects = useCallback((opts?: { initial?: boolean }) => {
    const initial = opts?.initial ?? false
    if (initial) setLoading(true)
    return api
      .listProjects()
      .then(setProjects)
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => {
        if (initial) setLoading(false)
      })
  }, [])

  useEffect(() => {
    const id = window.setTimeout(() => {
      void loadProjects({ initial: true })
    }, 0)
    return () => clearTimeout(id)
  }, [loadProjects])

  useEffect(() => {
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadProjects()
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.projects],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [loadProjects])

  const handleCreate = async () => {
    if (!form.name.trim()) return
    setCreating(true)
    setError(null)
    try {
      const project = await api.createProject({
        name: form.name.trim(),
        github_repository: form.github_repository.trim() || undefined,
        dockerfile_path: form.dockerfile_path.trim() || "Dockerfile",
        root_directory: form.root_directory.trim() || "/",
        desired_instance_count: Math.max(1, Math.floor(Number(form.desired_instance_count)) || 1),
      })
      setDialogOpen(false)
      setForm({
        name: "",
        github_repository: "",
        dockerfile_path: "Dockerfile",
        root_directory: "/",
        desired_instance_count: 1,
      })
      navigate(`/projects/${project.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Failed to create project")
    } finally {
      setCreating(false)
    }
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-32 rounded-xl" />
          ))}
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6 max-w-6xl mx-auto w-full px-0">
      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <h1 className="text-2xl font-semibold tracking-tight">Projects</h1>
        <Button size="sm" className="shrink-0 self-start sm:self-auto" onClick={() => setDialogOpen(true)}>
          <PlusIcon className="mr-2 size-4" />
          New Project
        </Button>
      </div>

      {projects.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12 text-center px-4">
            <FolderIcon className="size-10 text-muted-foreground mb-4" />
            <p className="text-muted-foreground">No projects yet.</p>
            <p className="text-sm text-muted-foreground mt-1 max-w-sm">
              Create a project with an optional GitHub repository. You’ll get webhook setup steps on the project
              page to deploy on push to main.
            </p>
            <Button size="sm" className="mt-4" onClick={() => setDialogOpen(true)}>
              <PlusIcon className="mr-2 size-4" />
              New Project
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {projects.map((project) => (
            <Link key={project.id} to={`/projects/${project.id}`} className="block min-w-0">
              <Card className="hover:bg-accent/50 transition-colors cursor-pointer h-full">
                <CardHeader className="pb-3">
                  <CardTitle className="text-base truncate">{project.name}</CardTitle>
                </CardHeader>
                <CardContent>
                  {project.github_repository ? (
                    <Badge variant="secondary" className="font-mono text-xs max-w-full truncate">
                      {project.github_repository}
                    </Badge>
                  ) : (
                    <span className="text-xs text-muted-foreground">No repository linked</span>
                  )}
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>New Project</DialogTitle>
            <DialogDescription>
              Name is required. Link a GitHub repo to enable push-to-deploy (main/master). Dockerfile path and root
              directory match your repo layout.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="name">Name</Label>
              <Input
                id="name"
                placeholder="my-app"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                onKeyDown={(e) => e.key === "Enter" && handleCreate()}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="repo">GitHub repository</Label>
              <Input
                id="repo"
                placeholder="owner/repo (optional)"
                className="font-mono text-sm"
                value={form.github_repository}
                onChange={(e) => setForm({ ...form, github_repository: e.target.value })}
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="dockerfile">Dockerfile path</Label>
                <Input
                  id="dockerfile"
                  placeholder="Dockerfile"
                  className="font-mono text-sm"
                  value={form.dockerfile_path}
                  onChange={(e) => setForm({ ...form, dockerfile_path: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="root">Root directory</Label>
                <Input
                  id="root"
                  placeholder="/"
                  className="font-mono text-sm"
                  value={form.root_directory}
                  onChange={(e) => setForm({ ...form, root_directory: e.target.value })}
                />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="instances">Desired instance count</Label>
              <Input
                id="instances"
                type="number"
                min={1}
                className="font-mono text-sm max-w-[140px]"
                value={form.desired_instance_count}
                onChange={(e) =>
                  setForm({ ...form, desired_instance_count: Number(e.target.value) || 1 })
                }
              />
              <p className="text-xs text-muted-foreground">Replicas for each running deployment (default 1).</p>
            </div>
          </div>
          <DialogFooter className="flex-col gap-2 sm:flex-row sm:gap-0">
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleCreate} disabled={!form.name.trim() || creating}>
              {creating ? "Creating..." : "Create Project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
