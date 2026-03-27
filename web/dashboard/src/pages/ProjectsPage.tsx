import { useCallback, useEffect, useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { api, type Project, APIError, dashboardEventTopics, subscribeDashboardEvents } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
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
import { FolderIcon, PlusIcon, ChevronRightIcon } from "lucide-react"
import {
  PageContainer,
  PageHeader,
  PageTitle,
  PageActions,
  PageSection,
  EmptyState,
  PageErrorBanner,
} from "@/components/page-layout"
import { Surface, SurfaceBody } from "@/components/page-surface"

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
      <PageContainer size="wide">
        <div className="space-y-6">
          <Skeleton className="h-8 w-48" />
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {[1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-[88px] rounded-xl" />
            ))}
          </div>
        </div>
      </PageContainer>
    )
  }

  return (
    <PageContainer size="wide">
      <PageSection>
        {error && <PageErrorBanner message={error} />}

        <PageHeader>
          <PageTitle>Projects</PageTitle>
          <PageActions>
            <Button size="sm" onClick={() => setDialogOpen(true)}>
              <PlusIcon className="mr-2 size-4" />
              New Project
            </Button>
          </PageActions>
        </PageHeader>

        {projects.length === 0 ? (
          <Surface>
            <EmptyState
              icon={<FolderIcon className="size-10" />}
              title="No projects yet"
              description="Create a project with an optional GitHub repository. You'll get webhook setup steps on the project page to deploy on push to main."
              action={
                <Button size="sm" onClick={() => setDialogOpen(true)}>
                  <PlusIcon className="mr-2 size-4" />
                  New Project
                </Button>
              }
            />
          </Surface>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {projects.map((project) => (
              <Link key={project.id} to={`/projects/${project.id}`} className="group block min-w-0">
                <Surface className="h-full transition-colors group-hover:border-foreground/20 group-hover:bg-accent/40">
                  <SurfaceBody className="flex items-start justify-between gap-3 pt-5 sm:pt-6">
                    <div className="min-w-0 space-y-2">
                      <p className="font-medium text-sm truncate">{project.name}</p>
                      {project.github_repository ? (
                        <Badge variant="secondary" className="font-mono text-xs max-w-full truncate">
                          {project.github_repository}
                        </Badge>
                      ) : (
                        <span className="text-xs text-muted-foreground">No repository linked</span>
                      )}
                    </div>
                    <ChevronRightIcon className="size-4 text-muted-foreground/50 shrink-0 mt-0.5 transition-transform group-hover:translate-x-0.5" />
                  </SurfaceBody>
                </Surface>
              </Link>
            ))}
          </div>
        )}
      </PageSection>

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
    </PageContainer>
  )
}
