import { useEffect, useState, useCallback } from "react"
import { useParams, Link, useNavigate } from "react-router-dom"
import { api, type Project, type Deployment, type GitHubSetup, type GitHead, APIError } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ArrowLeftIcon,
  RocketIcon,
  TrashIcon,
  CopyIcon,
  RefreshCwIcon,
  FolderGitIcon,
  LayoutListIcon,
  CloudDownloadIcon,
} from "lucide-react"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import { selectLatestRunningDeployment } from "@/lib/deployment-reachability"

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    console.warn("clipboard failed", label)
  }
}

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [project, setProject] = useState<Project | null>(null)
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [ghSetup, setGhSetup] = useState<GitHubSetup | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [deployDialogOpen, setDeployDialogOpen] = useState(false)
  const [deploying, setDeploying] = useState(false)
  const [commitSha, setCommitSha] = useState("")

  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [rotating, setRotating] = useState(false)

  const [gitHead, setGitHead] = useState<GitHead | null>(null)
  const [gitHeadLoading, setGitHeadLoading] = useState(false)
  const [sourceCheckError, setSourceCheckError] = useState<string | null>(null)
  const [gitHeadRef, setGitHeadRef] = useState("")
  const [fetchingHeadForDialog, setFetchingHeadForDialog] = useState(false)

  const [desiredInstances, setDesiredInstances] = useState(1)
  const [scalingSaving, setScalingSaving] = useState(false)

  const loadGitHubSetup = useCallback(async (projectId: string, hasRepo: boolean) => {
    if (!hasRepo) {
      setGhSetup(null)
      return
    }
    try {
      const s = await api.getGitHubSetup(projectId)
      setGhSetup(s)
    } catch {
      setGhSetup(null)
    }
  }, [])

  useEffect(() => {
    if (!id) return
    setError(null)
    Promise.all([api.getProject(id), api.listDeployments(id)])
      .then(([p, d]) => {
        setProject(p)
        setDeployments(d)
        const di = p.desired_instance_count
        setDesiredInstances(typeof di === "number" && di >= 1 ? di : 1)
        void loadGitHubSetup(id, Boolean(p.github_repository?.trim()))
      })
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [id, loadGitHubSetup])

  const handleDeploy = async () => {
    if (!id) return
    setDeploying(true)
    setError(null)
    try {
      const dep = await api.triggerDeploy(id, commitSha.trim() || "main")
      setDeployDialogOpen(false)
      setCommitSha("")
      navigate(`/deployments/${dep.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Deploy failed")
    } finally {
      setDeploying(false)
    }
  }

  const handleCheckGitHead = async () => {
    if (!id) return
    setGitHeadLoading(true)
    setSourceCheckError(null)
    try {
      const h = await api.getGitHead(id, gitHeadRef.trim() || undefined)
      setGitHead(h)
    } catch (e) {
      setGitHead(null)
      setSourceCheckError(e instanceof APIError ? e.message : "Could not reach GitHub")
    } finally {
      setGitHeadLoading(false)
    }
  }

  const handleDeployGitHead = async (sha: string) => {
    if (!id) return
    setDeploying(true)
    setError(null)
    try {
      const dep = await api.triggerDeploy(id, sha)
      setDeployDialogOpen(false)
      setCommitSha("")
      navigate(`/deployments/${dep.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Deploy failed")
    } finally {
      setDeploying(false)
    }
  }

  const handleFetchLatestIntoDialog = async () => {
    if (!id) return
    setFetchingHeadForDialog(true)
    setError(null)
    try {
      const h = await api.getGitHead(id)
      setCommitSha(h.sha)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not resolve latest commit")
    } finally {
      setFetchingHeadForDialog(false)
    }
  }

  const handleDelete = async () => {
    if (!id) return
    setDeleting(true)
    try {
      await api.deleteProject(id)
      navigate("/projects")
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Delete failed")
    } finally {
      setDeleting(false)
    }
  }

  const handleSaveScaling = async () => {
    if (!id) return
    const n = Math.max(1, Math.floor(Number(desiredInstances)) || 1)
    setScalingSaving(true)
    setError(null)
    try {
      const p = await api.patchProject(id, { desired_instance_count: n })
      setProject(p)
      setDesiredInstances(p.desired_instance_count ?? n)
      const d = await api.listDeployments(id)
      setDeployments(d)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not update instance count")
    } finally {
      setScalingSaving(false)
    }
  }

  const handleRotateSecret = async () => {
    if (!id) return
    setRotating(true)
    try {
      const r = await api.rotateWebhookSecret(id)
      setGhSetup((prev) =>
        prev
          ? {
              ...prev,
              webhook_secret: r.github_webhook_secret,
              webhook_url: r.webhook_url || prev.webhook_url,
            }
          : prev,
      )
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Failed to rotate secret")
    } finally {
      setRotating(false)
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
      <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-6 text-destructive text-sm max-w-xl">
        {error}
      </div>
    )
  }

  if (!project) return null

  const latestRunningDeployment = selectLatestRunningDeployment(deployments)

  return (
    <div className="space-y-6 max-w-5xl mx-auto w-full">
      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <div>
        <Link
          to="/projects"
          className="text-sm text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
        >
          <ArrowLeftIcon className="size-3" /> Projects
        </Link>
        <div className="flex flex-col gap-3 mt-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <h1 className="text-2xl font-semibold tracking-tight break-words">{project.name}</h1>
            <p className="text-sm text-muted-foreground mt-1">Use the tabs below for details, GitHub, and deployments.</p>
          </div>
          <div className="flex flex-wrap gap-2 shrink-0">
            <Button size="sm" onClick={() => setDeployDialogOpen(true)}>
              <RocketIcon className="mr-2 size-4" />
              Deploy
            </Button>
            <Button size="sm" variant="outline" onClick={() => setDeleteDialogOpen(true)} aria-label="Delete project">
              <TrashIcon className="size-4" />
            </Button>
          </div>
        </div>
      </div>

      <Tabs defaultValue="overview" className="min-w-0">
        <TabsList variant="line" className="w-full min-w-0 max-w-full justify-start overflow-x-auto">
          <TabsTrigger value="overview" className="shrink-0">
            Overview
          </TabsTrigger>
          <TabsTrigger value="github" className="shrink-0">
            <FolderGitIcon className="size-4" />
            GitHub
          </TabsTrigger>
          <TabsTrigger value="deployments" className="shrink-0">
            <LayoutListIcon className="size-4" />
            Deployments
          </TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-4 space-y-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Project</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <div>
                <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Repository</p>
                <p className="font-mono text-sm mt-1 break-all">
                  {project.github_repository || "— Not linked —"}
                </p>
                {!project.github_repository && (
                  <p className="text-muted-foreground mt-2 text-xs">
                    Link a repo under <strong>GitHub</strong> by creating a project with a GitHub path, or recreate this
                    project with repository details to enable webhooks and push-to-deploy.
                  </p>
                )}
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Dockerfile</p>
                  <p className="font-mono text-sm mt-1">{project.dockerfile_path}</p>
                </div>
                <div>
                  <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Root directory</p>
                  <p className="font-mono text-sm mt-1">{project.root_directory}</p>
                </div>
              </div>
              <div className="border-t pt-4 space-y-2">
                <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Horizontal scaling</p>
                <p className="text-xs text-muted-foreground">
                  Desired replicas for the running deployment. Changes converge via the reconciler (may take a few
                  seconds).
                </p>
                {latestRunningDeployment != null ? (
                  <p className="text-sm">
                    Current:{" "}
                    <span className="font-mono">
                      {latestRunningDeployment.running_instance_count ?? 0} /{" "}
                      {latestRunningDeployment.desired_instance_count ?? project.desired_instance_count ?? 1} running
                    </span>
                  </p>
                ) : null}
                <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="space-y-1 flex-1 min-w-0">
                    <Label className="text-xs text-muted-foreground">Desired instance count</Label>
                    <Input
                      type="number"
                      min={1}
                      className="font-mono h-9 w-full sm:max-w-[120px]"
                      value={desiredInstances}
                      onChange={(e) => setDesiredInstances(Number(e.target.value))}
                    />
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={scalingSaving}
                    className="shrink-0"
                    onClick={() => void handleSaveScaling()}
                  >
                    {scalingSaving ? "Saving…" : "Save"}
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
          {latestRunningDeployment ? (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Current reachability</CardTitle>
                <p className="text-sm text-muted-foreground">
                  Latest running deployment:{" "}
                  <span className="font-mono">
                    {latestRunningDeployment.github_commit ? latestRunningDeployment.github_commit.slice(0, 8) : "manual"}
                  </span>
                </p>
              </CardHeader>
              <CardContent className="space-y-4">
                <DeploymentReachability reachable={latestRunningDeployment.reachable} compact />
                <Link
                  to={`/deployments/${latestRunningDeployment.id}`}
                  className="inline-flex text-sm text-primary underline-offset-4 hover:underline"
                >
                  View deployment details
                </Link>
              </CardContent>
            </Card>
          ) : null}
        </TabsContent>

        <TabsContent value="github" className="mt-4">
          {!project.github_repository ? (
            <Card>
              <CardContent className="py-10 text-center text-sm text-muted-foreground px-4">
                No GitHub repository is linked to this project. Webhook setup applies only when a repo is configured.
              </CardContent>
            </Card>
          ) : !ghSetup ? (
            <Card>
              <CardContent className="py-10 text-center text-sm text-muted-foreground">Loading GitHub setup…</CardContent>
            </Card>
          ) : (
            <Card>
              <CardHeader className="pb-3">
                <CardTitle className="text-base">GitHub webhook</CardTitle>
                <p className="text-sm text-muted-foreground font-normal">{ghSetup.instructions}</p>
              </CardHeader>
              <CardContent className="space-y-4">
                {!ghSetup.public_base_url_configured && (
                  <div className="rounded-lg border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-100">
                    Set the public base URL under <strong>Settings → Public URL</strong> so the GitHub webhook payload
                    URL is absolute (stored in the database).
                  </div>
                )}
                <div className="space-y-1">
                  <Label className="text-xs text-muted-foreground">Payload URL</Label>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code className="flex-1 rounded-md border bg-muted/50 px-3 py-2 text-xs font-mono break-all">
                      {ghSetup.webhook_url || `(your-host)${ghSetup.webhook_path}`}
                    </code>
                    {ghSetup.webhook_url ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="shrink-0"
                        onClick={() => void copyText("webhook", ghSetup.webhook_url)}
                      >
                        <CopyIcon className="mr-2 size-3" /> Copy
                      </Button>
                    ) : null}
                  </div>
                </div>
                <div className="space-y-1">
                  <div className="flex items-center justify-between gap-2">
                    <Label className="text-xs text-muted-foreground">Webhook secret</Label>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={rotating}
                      onClick={() => void handleRotateSecret()}
                    >
                      <RefreshCwIcon className={`mr-1 size-3 ${rotating ? "animate-spin" : ""}`} />
                      Rotate
                    </Button>
                  </div>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code className="flex-1 rounded-md border bg-muted/50 px-3 py-2 text-xs font-mono break-all">
                      {ghSetup.webhook_secret || "—"}
                    </code>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="shrink-0"
                      disabled={!ghSetup.webhook_secret}
                      onClick={() => void copyText("secret", ghSetup.webhook_secret)}
                    >
                      <CopyIcon className="mr-2 size-3" /> Copy secret
                    </Button>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    After rotating, update the secret in GitHub. New projects generate a secret when a repo is linked.
                  </p>
                </div>

                <div className="border-t pt-4 mt-2 space-y-3">
                  <div>
                    <p className="text-sm font-medium">Without a webhook</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Resolve the tip of a branch from the GitHub API, compare it to your running deployment, then deploy
                      that commit. Set <code className="font-mono text-[0.7rem]">GITHUB_TOKEN</code> on the kindling server
                      for private repositories.
                    </p>
                  </div>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                    <div className="flex-1 space-y-1 min-w-0">
                      <Label className="text-xs text-muted-foreground">Optional ref (branch or tag)</Label>
                      <Input
                        value={gitHeadRef}
                        onChange={(e) => setGitHeadRef(e.target.value)}
                        placeholder="repository default branch"
                        className="font-mono text-sm h-9"
                      />
                    </div>
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      className="shrink-0"
                      disabled={gitHeadLoading}
                      onClick={() => void handleCheckGitHead()}
                    >
                      <CloudDownloadIcon className={`mr-2 size-4 ${gitHeadLoading ? "animate-pulse" : ""}`} />
                      {gitHeadLoading ? "Checking…" : "Check for updates"}
                    </Button>
                  </div>
                  {sourceCheckError ? (
                    <p className="text-xs text-destructive">{sourceCheckError}</p>
                  ) : null}
                  {gitHead ? (
                    <div className="rounded-lg border bg-muted/30 px-3 py-3 text-sm space-y-2">
                      <p>
                        <span className="text-muted-foreground">Tip of </span>
                        <span className="font-mono">{gitHead.ref}</span>
                        <span className="text-muted-foreground"> → </span>
                        <span className="font-mono">{gitHead.short_sha}</span>
                      </p>
                      <p className="text-xs">
                        {gitHead.update_available ? (
                          <span className="text-amber-800 dark:text-amber-200">
                            Differs from your running deployment or latest recorded deployment — a new deploy may be
                            warranted.
                          </span>
                        ) : (
                          <span className="text-muted-foreground">Matches what is already running or last deployed for this ref.</span>
                        )}
                      </p>
                      {!gitHead.github_token_configured ? (
                        <p className="text-xs text-amber-900/90 dark:text-amber-100/90">
                          No <code className="font-mono">GITHUB_TOKEN</code> on the server: public repos only unless you set
                          the token in the kindling process environment.
                        </p>
                      ) : null}
                      <Button size="sm" onClick={() => void handleDeployGitHead(gitHead.sha)} disabled={deploying}>
                        {deploying ? "Deploying…" : `Deploy ${gitHead.short_sha}`}
                      </Button>
                    </div>
                  ) : null}
                </div>
              </CardContent>
            </Card>
          )}
        </TabsContent>

        <TabsContent value="deployments" className="mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Deployments</CardTitle>
            </CardHeader>
            <CardContent>
              {deployments.length === 0 ? (
                <div className="flex flex-col items-center justify-center py-8 text-center px-2">
                  <RocketIcon className="size-8 text-muted-foreground mb-3" />
                  <p className="text-sm text-muted-foreground">No deployments yet.</p>
                  <Button size="sm" className="mt-3" onClick={() => setDeployDialogOpen(true)}>
                    Deploy now
                  </Button>
                </div>
              ) : (
                <div className="space-y-3">
                  {deployments.map((dep) => (
                    <Link key={dep.id} to={`/deployments/${dep.id}`} className="block">
                      <div className="flex flex-col gap-2 rounded-lg border p-3 hover:bg-accent/50 transition-colors sm:flex-row sm:items-center sm:justify-between min-w-0">
                        <div className="flex flex-wrap items-center gap-2 min-w-0">
                          <Badge variant={phaseVariant(dep.phase)}>{phaseLabel(dep.phase)}</Badge>
                          <span className="font-mono text-sm">
                            {dep.github_commit ? dep.github_commit.slice(0, 8) : "manual"}
                          </span>
                          {dep.build_status && (
                            <span className="text-xs text-muted-foreground hidden sm:inline">Build: {dep.build_status}</span>
                          )}
                        </div>
                        <span className="text-xs text-muted-foreground shrink-0">
                          {dep.created_at ? new Date(dep.created_at).toLocaleString() : ""}
                        </span>
                      </div>
                    </Link>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <Dialog open={deployDialogOpen} onOpenChange={setDeployDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deploy</DialogTitle>
            <DialogDescription>Trigger a new deployment for {project.name}.</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="commit">Branch or commit SHA</Label>
            <Input
              id="commit"
              placeholder="main"
              value={commitSha}
              onChange={(e) => setCommitSha(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleDeploy()}
              className="font-mono"
            />
            <p className="text-xs text-muted-foreground">Defaults to main if left empty.</p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-full sm:w-auto"
              disabled={deploying || fetchingHeadForDialog || !project.github_repository?.trim()}
              onClick={() => void handleFetchLatestIntoDialog()}
            >
              <CloudDownloadIcon className="mr-2 size-4" />
              {fetchingHeadForDialog ? "Fetching…" : "Fill with latest from GitHub"}
            </Button>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeployDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => void handleDeploy()} disabled={deploying}>
              {deploying ? "Deploying..." : "Deploy"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Project</DialogTitle>
            <DialogDescription>
              Are you sure you want to delete <strong>{project.name}</strong>? This will remove all deployments and
              cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialogOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void handleDelete()} disabled={deleting}>
              {deleting ? "Deleting..." : "Delete Project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
