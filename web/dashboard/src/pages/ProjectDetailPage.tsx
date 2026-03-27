import { useEffect, useState, useCallback } from "react"
import { useParams, Link, useNavigate } from "react-router-dom"
import {
  api,
  type Project,
  type Deployment,
  type PreviewEnvironment,
  type GitHubSetup,
  type GitHead,
  type ProjectDomain,
  type ProjectVolume,
  type ProjectSecret,
  type UsageCurrent,
  type UsageHistory,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
} from "@/lib/api"
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
  RocketIcon,
  TrashIcon,
  CopyIcon,
  RefreshCwIcon,
  FolderGitIcon,
  LayoutListIcon,
  CloudDownloadIcon,
  GlobeIcon,
  BarChart3Icon,
  ChevronRightIcon,
  GitPullRequestIcon,
  KeyRoundIcon,
  HardDriveIcon,
} from "lucide-react"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import { upsertProjectSecretInList } from "@/lib/project-secrets"
import { selectLatestRunningDeployment } from "@/lib/deployment-reachability"
import {
  PageContainer,
  PageHeader,
  PageTitle,
  PageDescription,
  PageActions,
  PageBackLink,
  PageSection,
  MetadataGrid,
  MetadataItem,
  EmptyState,
  PageErrorBanner,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceDescription,
  SurfaceBody,
  SurfaceSeparator,
} from "@/components/page-surface"

const DEPLOYMENTS_PER_PAGE = 10

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    console.warn("clipboard failed", label)
  }
}

function previewLifecycleLabel(state: PreviewEnvironment["lifecycle_state"]): string {
  switch (state) {
    case "active":
      return "Active"
    case "cleanup_due":
      return "Cleanup due"
    default:
      return "Closed"
  }
}

function previewLifecycleVariant(state: PreviewEnvironment["lifecycle_state"]): "default" | "secondary" | "destructive" {
  switch (state) {
    case "active":
      return "default"
    case "cleanup_due":
      return "destructive"
    default:
      return "secondary"
  }
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B"
  const u = ["B", "KB", "MB", "GB", "TB"]
  let v = n
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

function MiniBars({ values, label }: { values: number[]; label?: string }) {
  const max = Math.max(1, ...values)
  return (
    <div>
      {label != null && label !== "" ? <p className="stat-label mb-2">{label}</p> : null}
      <div className="flex items-end gap-px h-20 w-full border rounded-lg p-2 bg-muted/20">
        {values.length === 0 ? (
          <p className="text-xs text-muted-foreground w-full text-center self-center">No data yet</p>
        ) : (
          values.map((v, i) => (
            <div
              key={i}
              className="flex-1 min-w-[2px] bg-primary/70 rounded-t-sm transition-[height]"
              style={{ height: `${Math.max(6, (v / max) * 100)}%` }}
              title={String(v)}
            />
          ))
        )}
      </div>
    </div>
  )
}

export function ProjectDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [project, setProject] = useState<Project | null>(null)
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [deploymentsPage, setDeploymentsPage] = useState(1)
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
  const [buildRuleSaving, setBuildRuleSaving] = useState(false)

  const [domains, setDomains] = useState<ProjectDomain[]>([])
  const [domainsLoading, setDomainsLoading] = useState(false)
  const [newDomainName, setNewDomainName] = useState("")
  const [domainSaving, setDomainSaving] = useState(false)
  const [verifyingId, setVerifyingId] = useState<string | null>(null)
  const [projectVolume, setProjectVolume] = useState<ProjectVolume | null>(null)
  const [volumeMountPath, setVolumeMountPath] = useState("/data")
  const [volumeSizeGB, setVolumeSizeGB] = useState(10)
  const [volumeSaving, setVolumeSaving] = useState(false)
  const [volumeDeleting, setVolumeDeleting] = useState(false)
  const [secrets, setSecrets] = useState<ProjectSecret[]>([])
  const [secretsLoading, setSecretsLoading] = useState(false)
  const [secretName, setSecretName] = useState("")
  const [secretValue, setSecretValue] = useState("")
  const [secretSaving, setSecretSaving] = useState(false)
  const [deletingSecretId, setDeletingSecretId] = useState<string | null>(null)

  const [mainTab, setMainTab] = useState("overview")
  const [usageCurrent, setUsageCurrent] = useState<UsageCurrent | null>(null)
  const [usageHistory, setUsageHistory] = useState<UsageHistory | null>(null)
  const [usageLoading, setUsageLoading] = useState(false)
  const [usageWindow, setUsageWindow] = useState<"1h" | "24h" | "7d">("24h")

  const [previews, setPreviews] = useState<PreviewEnvironment[]>([])
  const [previewsLoading, setPreviewsLoading] = useState(false)
  const [previewActionId, setPreviewActionId] = useState<string | null>(null)
  const [previewActionKind, setPreviewActionKind] = useState<"redeploy" | "delete" | null>(null)

  const loadPreviews = useCallback(async () => {
    if (!id) return
    setPreviewsLoading(true)
    try {
      const list = await api.listProjectPreviews(id)
      setPreviews(list)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load PR previews")
      setPreviews([])
    } finally {
      setPreviewsLoading(false)
    }
  }, [id])

  const loadUsage = useCallback(async () => {
    if (!id) return
    setUsageLoading(true)
    try {
      const [c, h] = await Promise.all([
        api.getProjectUsageCurrent(id),
        api.getProjectUsageHistory(id, usageWindow),
      ])
      setUsageCurrent(c)
      setUsageHistory(h)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load usage")
      setUsageCurrent(null)
      setUsageHistory(null)
    } finally {
      setUsageLoading(false)
    }
  }, [id, usageWindow])

  const loadSecrets = useCallback(async (projectId: string) => {
    setSecretsLoading(true)
    try {
      const list = await api.listProjectSecrets(projectId)
      setSecrets(list)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load secrets")
      setSecrets([])
    } finally {
      setSecretsLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!id || mainTab !== "usage") return
    void loadUsage()
  }, [id, mainTab, loadUsage])

  useEffect(() => {
    if (!id || mainTab !== "previews") return
    void loadPreviews()
  }, [id, mainTab, loadPreviews])

  useEffect(() => {
    if (!id || mainTab !== "secrets") return
    void loadSecrets(id)
  }, [id, mainTab, loadSecrets])

  useEffect(() => {
    if (!id || mainTab !== "previews") return
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadPreviews()
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.project(id), dashboardEventTopics.projectDeployments(id)],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [id, mainTab, loadPreviews])

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

  const loadDomains = useCallback(async (projectId: string) => {
    setDomainsLoading(true)
    try {
      const list = await api.listProjectDomains(projectId)
      setDomains(list)
    } catch {
      setDomains([])
    } finally {
      setDomainsLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!id || mainTab !== "secrets") return
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadSecrets(id)
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.project(id)],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [id, mainTab, loadSecrets])

  const loadProjectPage = useCallback(
    (opts?: { initial?: boolean }) => {
      if (!id) return Promise.resolve()
      const initial = opts?.initial ?? false
      if (initial) {
        setLoading(true)
        setError(null)
      }
      return Promise.all([
        api.getProject(id),
        api.listDeployments(id),
        api.getProjectVolume(id).catch((e) => {
          if (e instanceof APIError && e.status === 404) {
            return null
          }
          throw e
        }),
      ])
        .then(([p, d, v]) => {
          setProject(p)
          setDeployments(d)
          setProjectVolume(v)
          setVolumeMountPath(v?.mount_path || "/data")
          setVolumeSizeGB(v?.size_gb ?? 10)
          const di = p.desired_instance_count
          setDesiredInstances(typeof di === "number" && di >= 1 ? di : 1)
          void loadGitHubSetup(id, Boolean(p.github_repository?.trim()))
          void loadDomains(id)
        })
        .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
        .finally(() => {
          if (initial) setLoading(false)
        })
    },
    [id, loadGitHubSetup, loadDomains],
  )

  useEffect(() => {
    const id = window.setTimeout(() => {
      void loadProjectPage({ initial: true })
    }, 0)
    return () => clearTimeout(id)
  }, [loadProjectPage])

  useEffect(() => {
    setDeploymentsPage(1)
    setProjectVolume(null)
    setVolumeMountPath("/data")
    setVolumeSizeGB(10)
    setSecrets([])
    setSecretName("")
    setSecretValue("")
  }, [id])

  useEffect(() => {
    const totalPages = Math.max(1, Math.ceil(deployments.length / DEPLOYMENTS_PER_PAGE))
    setDeploymentsPage((page) => Math.min(page, totalPages))
  }, [deployments.length])

  useEffect(() => {
    if (!id) return
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadProjectPage()
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.project(id), dashboardEventTopics.projectDeployments(id)],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [id, loadProjectPage])

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

  const handleRedeployPreview = async (previewId: string) => {
    if (!id) return
    setPreviewActionId(previewId)
    setPreviewActionKind("redeploy")
    setError(null)
    try {
      const dep = await api.redeployProjectPreview(id, previewId)
      await loadPreviews()
      navigate(`/deployments/${dep.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not redeploy preview")
    } finally {
      setPreviewActionId(null)
      setPreviewActionKind(null)
    }
  }

  const handleDeletePreview = async (previewId: string) => {
    if (!id) return
    setPreviewActionId(previewId)
    setPreviewActionKind("delete")
    setError(null)
    try {
      await api.deleteProjectPreview(id, previewId)
      await loadPreviews()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete preview")
    } finally {
      setPreviewActionId(null)
      setPreviewActionKind(null)
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

  const handleBuildOnlyOnRootChangesChange = async (checked: boolean) => {
    if (!id) return
    setBuildRuleSaving(true)
    setError(null)
    try {
      const p = await api.patchProject(id, { build_only_on_root_changes: checked })
      setProject(p)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not update build rules")
    } finally {
      setBuildRuleSaving(false)
    }
  }

  const handleSaveVolume = async () => {
    if (!id) return
    setVolumeSaving(true)
    setError(null)
    try {
      const volume = await api.putProjectVolume(id, {
        mount_path: volumeMountPath.trim() || "/data",
        size_gb: Math.max(1, Math.floor(Number(volumeSizeGB)) || 10),
      })
      setProjectVolume(volume)
      setVolumeMountPath(volume.mount_path)
      setVolumeSizeGB(volume.size_gb)
      const [p, d] = await Promise.all([api.getProject(id), api.listDeployments(id)])
      setProject(p)
      setDeployments(d)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not save persistent volume")
    } finally {
      setVolumeSaving(false)
    }
  }

  const handleDeleteVolume = async () => {
    if (!id) return
    setVolumeDeleting(true)
    setError(null)
    try {
      await api.deleteProjectVolume(id)
      setProjectVolume(null)
      setVolumeMountPath("/data")
      setVolumeSizeGB(10)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete persistent volume")
    } finally {
      setVolumeDeleting(false)
    }
  }

  const handleAddDomain = async () => {
    if (!id) return
    const name = newDomainName.trim()
    if (!name) return
    setDomainSaving(true)
    setError(null)
    try {
      await api.createProjectDomain(id, name)
      setNewDomainName("")
      await loadDomains(id)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not add domain")
    } finally {
      setDomainSaving(false)
    }
  }

  const handleVerifyDomain = async (domainId: string) => {
    if (!id) return
    setVerifyingId(domainId)
    setError(null)
    try {
      await api.verifyProjectDomain(id, domainId)
      await loadDomains(id)
      const d = await api.listDeployments(id)
      setDeployments(d)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Verification failed")
    } finally {
      setVerifyingId(null)
    }
  }

  const handleDeleteDomain = async (domainId: string) => {
    if (!id) return
    setError(null)
    try {
      await api.deleteProjectDomain(id, domainId)
      await loadDomains(id)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not remove domain")
    }
  }

  const handleSaveProjectSecret = async () => {
    if (!id) return
    const name = secretName.trim()
    if (!name) return
    setSecretSaving(true)
    setError(null)
    try {
      const saved = await api.upsertProjectSecret(id, { name, value: secretValue })
      setSecrets((current) => upsertProjectSecretInList(current, saved))
      setSecretName("")
      setSecretValue("")
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not save secret")
    } finally {
      setSecretSaving(false)
    }
  }

  const handleDeleteProjectSecret = async (secretId: string) => {
    if (!id) return
    setDeletingSecretId(secretId)
    setError(null)
    try {
      await api.deleteProjectSecret(id, secretId)
      setSecrets((current) => current.filter((secret) => secret.id !== secretId))
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete secret")
    } finally {
      setDeletingSecretId(null)
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
      <PageContainer size="wide">
        <div className="space-y-4">
          <Skeleton className="h-5 w-24" />
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-32 rounded-xl" />
          <Skeleton className="h-48 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  if (error && !project) {
    return (
      <PageContainer size="wide">
        <PageErrorBanner message={error} className="max-w-xl" />
      </PageContainer>
    )
  }

  if (!project) return null

  const latestRunningDeployment = selectLatestRunningDeployment(deployments)
  const totalDeploymentPages = Math.max(1, Math.ceil(deployments.length / DEPLOYMENTS_PER_PAGE))
  const deploymentStart = (deploymentsPage - 1) * DEPLOYMENTS_PER_PAGE
  const pagedDeployments = deployments.slice(deploymentStart, deploymentStart + DEPLOYMENTS_PER_PAGE)
  const visibleDeploymentsStart = deployments.length === 0 ? 0 : deploymentStart + 1
  const visibleDeploymentsEnd = Math.min(deploymentStart + DEPLOYMENTS_PER_PAGE, deployments.length)

  return (
    <PageContainer size="wide">
      <PageSection>
        {error && <PageErrorBanner message={error} />}

        <div>
          <PageBackLink to="/projects">Projects</PageBackLink>
          <PageHeader>
            <div className="min-w-0">
              <PageTitle className="break-words">{project.name}</PageTitle>
              <PageDescription className="mt-1">
                Project configuration, GitHub integration, and deployment history.
              </PageDescription>
            </div>
            <PageActions>
              <Button size="sm" onClick={() => setDeployDialogOpen(true)}>
                <RocketIcon className="mr-2 size-4" />
                Deploy
              </Button>
              <Button size="sm" variant="outline" onClick={() => setDeleteDialogOpen(true)} aria-label="Delete project">
                <TrashIcon className="size-4" />
              </Button>
            </PageActions>
          </PageHeader>
        </div>

        <Tabs value={mainTab} onValueChange={setMainTab} orientation="vertical" className="min-w-0 md:items-start gap-4 md:gap-6">
          <TabsList variant="line" className="w-full overflow-x-auto md:w-48 shrink-0 md:sticky md:top-6">
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="github">
              <FolderGitIcon className="size-4" />
              GitHub
            </TabsTrigger>
            <TabsTrigger value="secrets">
              <KeyRoundIcon className="size-4" />
              Secrets
            </TabsTrigger>
            <TabsTrigger value="deployments">
              <LayoutListIcon className="size-4" />
              Deployments
            </TabsTrigger>
            <TabsTrigger value="previews">
              <GitPullRequestIcon className="size-4" />
              Previews
            </TabsTrigger>
            <TabsTrigger value="usage">
              <BarChart3Icon className="size-4" />
              Usage
            </TabsTrigger>
            <TabsTrigger value="domains">
              <GlobeIcon className="size-4" />
              Domains
            </TabsTrigger>
          </TabsList>

          {/* ── Overview ────────────────────────────────────── */}
          <TabsContent value="overview" className="space-y-4 min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Project</SurfaceTitle>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                <MetadataGrid>
                  <MetadataItem label="Repository" span="full">
                    <p className="font-mono text-sm break-all">
                      {project.github_repository || "— Not linked —"}
                    </p>
                    {!project.github_repository && (
                      <p className="text-muted-foreground mt-2 text-xs">
                        Link a repo under <strong>GitHub</strong> by creating a project with a GitHub path, or recreate
                        this project with repository details to enable webhooks and push-to-deploy.
                      </p>
                    )}
                  </MetadataItem>
                  <MetadataItem label="Dockerfile">
                    <p className="font-mono text-sm">{project.dockerfile_path}</p>
                  </MetadataItem>
                  <MetadataItem label="Root directory">
                    <p className="font-mono text-sm">{project.root_directory}</p>
                  </MetadataItem>
                </MetadataGrid>
              </SurfaceBody>
              <SurfaceSeparator />
              <SurfaceBody className="space-y-3">
                <div>
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Horizontal scaling</p>
                  <p className="text-xs text-muted-foreground mt-1">
                    Desired replicas for the running deployment. Changes converge via the reconciler.
                  </p>
                </div>
                {latestRunningDeployment != null && (
                  <p className="text-sm">
                    Current:{" "}
                    <span className="font-mono">
                      {latestRunningDeployment.running_instance_count ?? 0} /{" "}
                      {latestRunningDeployment.desired_instance_count ?? project.desired_instance_count ?? 1} running
                    </span>
                  </p>
                )}
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
              </SurfaceBody>
              <SurfaceSeparator />
              <SurfaceBody className="space-y-4">
                <div className="flex items-start gap-3">
                  <div className="rounded-md border bg-muted/40 p-2 shrink-0">
                    <HardDriveIcon className="size-4" />
                  </div>
                  <div className="min-w-0">
                    <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Persistent volume</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Attach one durable block volume at a mount path like <span className="font-mono">/data</span>.
                      Volume-backed projects run as a single active instance on one Cloud Hypervisor worker, and redeploys may briefly stop the old VM before the new one starts.
                    </p>
                  </div>
                </div>
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">Mount path</Label>
                    <Input
                      className="font-mono h-9"
                      value={volumeMountPath}
                      disabled={Boolean(projectVolume)}
                      onChange={(e) => setVolumeMountPath(e.target.value)}
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">Size (GB)</Label>
                    <Input
                      type="number"
                      min={1}
                      className="font-mono h-9"
                      value={volumeSizeGB}
                      onChange={(e) => setVolumeSizeGB(Number(e.target.value))}
                    />
                  </div>
                </div>
                {projectVolume ? (
                  <div className="rounded-lg border bg-muted/20 p-3 space-y-2 text-sm">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={projectVolume.status === "attached" ? "default" : "secondary"}>
                        {projectVolume.status}
                      </Badge>
                      <span className="font-mono">{projectVolume.filesystem}</span>
                    </div>
                    <p>
                      Pinned server:{" "}
                      <span className="font-mono">{projectVolume.server_id || "unassigned"}</span>
                    </p>
                    <p>
                      Attached VM:{" "}
                      <span className="font-mono">{projectVolume.attached_vm_id || "detached"}</span>
                    </p>
                    {projectVolume.last_error ? (
                      <p className="text-xs text-destructive">{projectVolume.last_error}</p>
                    ) : null}
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    No persistent volume attached yet. Saving one will pin this project to a single Cloud Hypervisor worker.
                  </p>
                )}
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={volumeSaving}
                    onClick={() => void handleSaveVolume()}
                  >
                    {volumeSaving ? "Saving…" : projectVolume ? "Update volume" : "Enable volume"}
                  </Button>
                  {projectVolume ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={volumeDeleting || projectVolume.status === "attached"}
                      onClick={() => void handleDeleteVolume()}
                    >
                      {volumeDeleting ? "Deleting…" : "Delete volume"}
                    </Button>
                  ) : null}
                </div>
              </SurfaceBody>
              <SurfaceSeparator />
              <SurfaceBody className="space-y-3">
                <div>
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Smart builds</p>
                  <p className="text-xs text-muted-foreground mt-1">
                    When enabled, GitHub push deploys are skipped unless the push changes files under{" "}
                    <span className="font-mono">{project.root_directory}</span>.
                  </p>
                </div>
                <label className="flex items-start gap-3 rounded-lg border p-3">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4"
                    checked={Boolean(project.build_only_on_root_changes)}
                    disabled={buildRuleSaving}
                    onChange={(e) => void handleBuildOnlyOnRootChangesChange(e.target.checked)}
                  />
                  <div className="min-w-0">
                    <p className="text-sm font-medium">Only build when root directory changes</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Useful for monorepos like this landing site: commits outside the hosted root directory will not
                      trigger a production deploy.
                    </p>
                    {buildRuleSaving && <p className="text-xs text-muted-foreground mt-2">Saving…</p>}
                  </div>
                </label>
              </SurfaceBody>
            </Surface>

            {latestRunningDeployment && (
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Current reachability</SurfaceTitle>
                  <SurfaceDescription>
                    Latest running deployment:{" "}
                    <span className="font-mono">
                      {latestRunningDeployment.github_commit ? latestRunningDeployment.github_commit.slice(0, 8) : "manual"}
                    </span>
                  </SurfaceDescription>
                </SurfaceHeader>
                <SurfaceBody className="space-y-4">
                  <DeploymentReachability reachable={latestRunningDeployment.reachable} compact />
                  <Link
                    to={`/deployments/${latestRunningDeployment.id}`}
                    className="inline-flex text-sm text-primary underline-offset-4 hover:underline"
                  >
                    View deployment details
                  </Link>
                </SurfaceBody>
              </Surface>
            )}
          </TabsContent>

          {/* ── Domains ─────────────────────────────────────── */}
          <TabsContent value="domains" className="space-y-4 min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Custom domain</SurfaceTitle>
                <SurfaceDescription>
                  Put your own hostname (like <span className="font-mono">www.example.com</span>) in front of this
                  project. You only need DNS: one A record for traffic, one TXT record to prove you own the name.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-6">
                <div className="rounded-lg border bg-muted/30 px-4 py-3.5 text-sm">
                  <p className="font-medium text-foreground mb-2">Steps</p>
                  <ol className="list-decimal list-inside space-y-2 text-muted-foreground [&>li]:ps-1">
                    <li><span className="text-foreground">Deploy</span> this project so it is running.</li>
                    <li>
                      At your DNS provider, add an <span className="font-medium text-foreground">A</span> (or{" "}
                      <span className="font-medium text-foreground">AAAA</span>) record: your hostname → the{" "}
                      <span className="font-medium text-foreground">public IP</span> of the Kindling server.
                    </li>
                    <li>Type that same hostname below and click <span className="font-medium text-foreground">Add domain</span>.</li>
                    <li>Add the <span className="font-medium text-foreground">TXT</span> record we show you. Wait a minute or two for DNS to update.</li>
                    <li>Click <span className="font-medium text-foreground">I&apos;ve added the TXT — verify</span>.</li>
                  </ol>
                  <p className="mt-3 text-xs text-muted-foreground border-t border-border/60 pt-3">
                    After verification, HTTPS works when Kindling is started with TLS on port 443 (Let&apos;s Encrypt).
                    Until then, you can still use the direct URL on the deployment page.
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="new-domain" className="text-sm">Hostname to connect</Label>
                  <p className="text-xs text-muted-foreground">Use the full name visitors will type, e.g. www.yourdomain.com</p>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <Input
                      id="new-domain"
                      placeholder="www.kindling.systems"
                      className="font-mono h-10 sm:max-w-md"
                      value={newDomainName}
                      onChange={(e) => setNewDomainName(e.target.value)}
                      onKeyDown={(e) => e.key === "Enter" && void handleAddDomain()}
                    />
                    <Button
                      type="button"
                      size="sm"
                      className="h-10 w-full sm:w-auto shrink-0"
                      disabled={domainSaving || !newDomainName.trim()}
                      onClick={() => void handleAddDomain()}
                    >
                      {domainSaving ? "Adding…" : "Add domain"}
                    </Button>
                  </div>
                </div>

                {domainsLoading ? (
                  <p className="text-sm text-muted-foreground">Loading domains…</p>
                ) : domains.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    No domains yet. Follow the steps above, then add your hostname.
                  </p>
                ) : (
                  <ul className="space-y-4">
                    {domains.map((dom) => (
                      <li key={dom.id} className="rounded-lg border p-4 space-y-4">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="font-mono text-sm font-medium break-all">{dom.domain_name}</span>
                          {dom.verified_at ? (
                            <Badge variant="default" className="shrink-0">Verified</Badge>
                          ) : (
                            <Badge variant="secondary" className="shrink-0">Waiting for TXT record</Badge>
                          )}
                        </div>

                        {dom.dns_challenge && (
                          <div className="space-y-3">
                            <div className="text-sm">
                              <p className="font-medium">Prove you own this name</p>
                              <ol className="mt-2 list-decimal list-inside space-y-1.5 text-muted-foreground text-xs [&>li]:ps-1">
                                <li>Open your DNS provider (Cloudflare, Namecheap, Route 53, etc.).</li>
                                <li>Create a <strong className="text-foreground">TXT</strong> record.</li>
                                <li><strong className="text-foreground">Name / Host</strong>: paste the first value below.</li>
                                <li><strong className="text-foreground">Value / Content</strong>: paste the second value (one long line, no quotes).</li>
                                <li>Save and wait a minute, then use the verify button.</li>
                              </ol>
                            </div>
                            <div className="rounded-lg bg-muted/40 p-3.5 space-y-3 text-sm">
                              <div>
                                <div className="flex items-center justify-between gap-2 mb-1">
                                  <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">TXT — Name / Host</span>
                                  <Button type="button" variant="ghost" size="sm" className="h-7 text-xs" onClick={() => void copyText("TXT name", dom.dns_challenge!.name)}>
                                    <CopyIcon className="size-3.5 mr-1" /> Copy
                                  </Button>
                                </div>
                                <p className="font-mono text-xs break-all leading-relaxed">{dom.dns_challenge.name}</p>
                              </div>
                              <div>
                                <div className="flex items-center justify-between gap-2 mb-1">
                                  <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">TXT — Value / Content</span>
                                  <Button type="button" variant="ghost" size="sm" className="h-7 text-xs" onClick={() => void copyText("TXT value", dom.dns_challenge!.value)}>
                                    <CopyIcon className="size-3.5 mr-1" /> Copy
                                  </Button>
                                </div>
                                <p className="font-mono text-xs break-all leading-relaxed">{dom.dns_challenge.value}</p>
                              </div>
                            </div>
                          </div>
                        )}

                        {dom.verified_at && (
                          <p className="text-xs text-muted-foreground">
                            Traffic for this hostname can be routed here once DNS points at your server and TLS is enabled.
                          </p>
                        )}

                        <div className="flex flex-wrap gap-2 pt-1">
                          {!dom.verified_at && (
                            <Button type="button" size="sm" variant="default" disabled={verifyingId === dom.id} onClick={() => void handleVerifyDomain(dom.id)}>
                              {verifyingId === dom.id ? "Checking DNS…" : "I've added the TXT — verify"}
                            </Button>
                          )}
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            className="text-destructive border-destructive/30 hover:bg-destructive/10"
                            onClick={() => void handleDeleteDomain(dom.id)}
                          >
                            Remove domain
                          </Button>
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>

          {/* ── GitHub ──────────────────────────────────────── */}
          <TabsContent value="github" className="min-w-0">
            {!project.github_repository ? (
              <Surface>
                <EmptyState
                  title="No GitHub repository linked"
                  description="Webhook setup applies only when a repo is configured."
                  className="py-12"
                />
              </Surface>
            ) : !ghSetup ? (
              <Surface>
                <div className="py-10 text-center text-sm text-muted-foreground">Loading GitHub setup…</div>
              </Surface>
            ) : (
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>GitHub webhook</SurfaceTitle>
                  <SurfaceDescription>{ghSetup.instructions}</SurfaceDescription>
                </SurfaceHeader>
                <SurfaceBody className="space-y-5">
                  {!ghSetup.public_base_url_configured && (
                    <div className="rounded-lg border border-amber-500/50 bg-amber-500/10 px-3.5 py-2.5 text-sm text-amber-900 dark:text-amber-100">
                      Set the public base URL under <strong>Settings → Public URL</strong> so the GitHub webhook payload URL is absolute.
                    </div>
                  )}
                  <div className="space-y-1.5">
                    <Label className="text-xs text-muted-foreground">Payload URL</Label>
                    <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                      <code className="code-block flex-1">{ghSetup.webhook_url || `(your-host)${ghSetup.webhook_path}`}</code>
                      {ghSetup.webhook_url && (
                        <Button type="button" variant="outline" size="sm" className="shrink-0" onClick={() => void copyText("webhook", ghSetup.webhook_url)}>
                          <CopyIcon className="mr-2 size-3" /> Copy
                        </Button>
                      )}
                    </div>
                  </div>
                  <div className="space-y-1.5">
                    <div className="flex items-center justify-between gap-2">
                      <Label className="text-xs text-muted-foreground">Webhook secret</Label>
                      <Button type="button" variant="ghost" size="sm" disabled={rotating} onClick={() => void handleRotateSecret()}>
                        <RefreshCwIcon className={`mr-1 size-3 ${rotating ? "animate-spin" : ""}`} /> Rotate
                      </Button>
                    </div>
                    <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                      <code className="code-block flex-1">{ghSetup.webhook_secret || "—"}</code>
                      <Button type="button" variant="outline" size="sm" className="shrink-0" disabled={!ghSetup.webhook_secret} onClick={() => void copyText("secret", ghSetup.webhook_secret)}>
                        <CopyIcon className="mr-2 size-3" /> Copy secret
                      </Button>
                    </div>
                    <p className="text-xs text-muted-foreground">After rotating, update the secret in GitHub.</p>
                  </div>
                </SurfaceBody>
                <SurfaceSeparator />
                <SurfaceBody className="space-y-4">
                  <div>
                    <p className="text-sm font-medium">Without a webhook</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Resolve the tip of a branch from the GitHub API, compare it to your running deployment, then deploy that commit.
                      Set <code className="font-mono text-[0.7rem]">GITHUB_TOKEN</code> on the kindling server for private repositories.
                    </p>
                  </div>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
                    <div className="flex-1 space-y-1 min-w-0">
                      <Label className="text-xs text-muted-foreground">Optional ref (branch or tag)</Label>
                      <Input value={gitHeadRef} onChange={(e) => setGitHeadRef(e.target.value)} placeholder="repository default branch" className="font-mono text-sm h-9" />
                    </div>
                    <Button type="button" variant="secondary" size="sm" className="shrink-0" disabled={gitHeadLoading} onClick={() => void handleCheckGitHead()}>
                      <CloudDownloadIcon className={`mr-2 size-4 ${gitHeadLoading ? "animate-pulse" : ""}`} />
                      {gitHeadLoading ? "Checking…" : "Check for updates"}
                    </Button>
                  </div>
                  {sourceCheckError && <p className="text-xs text-destructive">{sourceCheckError}</p>}
                  {gitHead && (
                    <div className="rounded-lg border bg-muted/30 px-3.5 py-3 text-sm space-y-2">
                      <p>
                        <span className="text-muted-foreground">Tip of </span>
                        <span className="font-mono">{gitHead.ref}</span>
                        <span className="text-muted-foreground"> → </span>
                        <span className="font-mono">{gitHead.short_sha}</span>
                      </p>
                      <p className="text-xs">
                        {gitHead.update_available ? (
                          <span className="text-amber-800 dark:text-amber-200">
                            Differs from your running deployment — a new deploy may be warranted.
                          </span>
                        ) : (
                          <span className="text-muted-foreground">Matches what is already running or last deployed.</span>
                        )}
                      </p>
                      {!gitHead.github_token_configured && (
                        <p className="text-xs text-amber-900/90 dark:text-amber-100/90">
                          No <code className="font-mono">GITHUB_TOKEN</code> on the server: public repos only.
                        </p>
                      )}
                      <Button size="sm" onClick={() => void handleDeployGitHead(gitHead.sha)} disabled={deploying}>
                        {deploying ? "Deploying…" : `Deploy ${gitHead.short_sha}`}
                      </Button>
                    </div>
                  )}
                </SurfaceBody>
              </Surface>
            )}
          </TabsContent>

          {/* ── Secrets ─────────────────────────────────────── */}
          <TabsContent value="secrets" className="space-y-4 min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Encrypted project secrets</SurfaceTitle>
                <SurfaceDescription>
                  Secrets are stored encrypted at rest and injected into deployments as environment variables.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-4">
                <div className="grid gap-3 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.2fr)_auto] lg:items-end">
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">Name</Label>
                    <Input
                      value={secretName}
                      onChange={(e) => setSecretName(e.target.value)}
                      placeholder="API_KEY"
                      className="font-mono text-sm h-9"
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">New value</Label>
                    <Input
                      type="password"
                      value={secretValue}
                      onChange={(e) => setSecretValue(e.target.value)}
                      placeholder="Enter a new value"
                      className="font-mono text-sm h-9"
                    />
                  </div>
                  <Button
                    type="button"
                    size="sm"
                    className="shrink-0"
                    disabled={secretSaving || secretName.trim() === ""}
                    onClick={() => void handleSaveProjectSecret()}
                  >
                    {secretSaving ? "Saving…" : "Save secret"}
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">
                  Stored values are write-only. Replacing a secret requires entering a new value. Leaving the value blank
                  stores an empty string.
                </p>
              </SurfaceBody>
            </Surface>

            {secretsLoading && secrets.length === 0 ? (
              <div className="space-y-3">
                <Skeleton className="h-20 rounded-xl" />
                <Skeleton className="h-20 rounded-xl" />
              </div>
            ) : secrets.length === 0 ? (
              <Surface>
                <EmptyState
                  icon={<KeyRoundIcon className="size-8" />}
                  title="No secrets yet"
                  description="Add project secrets here to inject encrypted environment variables into future deployments."
                  className="py-12"
                />
              </Surface>
            ) : (
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Current secret names</SurfaceTitle>
                  <SurfaceDescription>
                    Names and timestamps are visible here, but stored values are never shown again.
                  </SurfaceDescription>
                </SurfaceHeader>
                <SurfaceBody className="p-0">
                  <ul className="divide-y">
                    {secrets.map((secret) => (
                      <li key={secret.id} className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                        <div className="min-w-0">
                          <p className="font-mono text-sm break-all">{secret.name}</p>
                          <p className="text-xs text-muted-foreground">
                            Updated{" "}
                            {secret.updated_at != null && secret.updated_at !== ""
                              ? new Date(secret.updated_at).toLocaleString()
                              : secret.created_at != null && secret.created_at !== ""
                                ? new Date(secret.created_at).toLocaleString()
                                : "recently"}
                          </p>
                        </div>
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          className="shrink-0 text-destructive border-destructive/30 hover:bg-destructive/10"
                          disabled={deletingSecretId === secret.id}
                          onClick={() => void handleDeleteProjectSecret(secret.id)}
                        >
                          {deletingSecretId === secret.id ? "Deleting…" : "Delete"}
                        </Button>
                      </li>
                    ))}
                  </ul>
                </SurfaceBody>
              </Surface>
            )}
          </TabsContent>

          {/* ── Deployments ─────────────────────────────────── */}
          <TabsContent value="deployments" className="min-w-0">
            {deployments.length === 0 ? (
              <Surface>
                <EmptyState
                  icon={<RocketIcon className="size-8" />}
                  title="No deployments yet"
                  action={
                    <Button size="sm" onClick={() => setDeployDialogOpen(true)}>Deploy now</Button>
                  }
                  className="py-12"
                />
              </Surface>
            ) : (
              <Surface>
                <div className="flex flex-col gap-3 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                  <p className="text-xs text-muted-foreground">
                    Showing {visibleDeploymentsStart}-{visibleDeploymentsEnd} of {deployments.length} deployments
                  </p>
                  <div className="flex items-center gap-2">
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => setDeploymentsPage((page) => Math.max(1, page - 1))}
                      disabled={deploymentsPage <= 1}
                    >
                      Previous
                    </Button>
                    <span className="text-xs text-muted-foreground">
                      Page {deploymentsPage} of {totalDeploymentPages}
                    </span>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => setDeploymentsPage((page) => Math.min(totalDeploymentPages, page + 1))}
                      disabled={deploymentsPage >= totalDeploymentPages}
                    >
                      Next
                    </Button>
                  </div>
                </div>
                <ul className="divide-y">
                  {pagedDeployments.map((dep) => (
                    <li key={dep.id}>
                      <Link to={`/deployments/${dep.id}`} className="list-row group">
                        <div className="flex flex-wrap items-center gap-2 min-w-0">
                          <Badge variant={phaseVariant(dep.phase)}>{phaseLabel(dep.phase)}</Badge>
                          <span className="font-mono text-sm">{dep.github_commit ? dep.github_commit.slice(0, 8) : "manual"}</span>
                          {dep.build_status && <span className="text-xs text-muted-foreground hidden sm:inline">Build: {dep.build_status}</span>}
                        </div>
                        <div className="flex items-center gap-2">
                          <span className="text-xs text-muted-foreground shrink-0">{dep.created_at ? new Date(dep.created_at).toLocaleString() : ""}</span>
                          <ChevronRightIcon className="size-4 text-muted-foreground/40 shrink-0 transition-transform group-hover:translate-x-0.5" />
                        </div>
                      </Link>
                    </li>
                  ))}
                </ul>
              </Surface>
            )}
          </TabsContent>

          {/* ── PR previews ─────────────────────────────────── */}
          <TabsContent value="previews" className="space-y-4 min-w-0">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-sm text-muted-foreground max-w-xl">
                <span className="font-medium text-foreground">PR preview</span> URLs are created from GitHub{" "}
                <code className="font-mono text-xs">pull_request</code> events when{" "}
                <strong className="text-foreground">Preview base domain</strong> is set in{" "}
                <Link to="/settings" className="text-primary underline-offset-4 hover:underline">
                  Settings
                </Link>
                . Active previews can redeploy on demand, close immediately when the PR closes, and remain visible
                here until automatic cleanup removes them.
              </p>
              <Button type="button" size="sm" variant="secondary" onClick={() => void loadPreviews()} disabled={previewsLoading}>
                <RefreshCwIcon className={`mr-2 size-4 ${previewsLoading ? "animate-spin" : ""}`} /> Refresh
              </Button>
            </div>

            {!project.github_repository?.trim() ? (
              <Surface>
                <EmptyState
                  icon={<GitPullRequestIcon className="size-8" />}
                  title="Link a GitHub repository"
                  description="PR previews require a repository and the GitHub webhook (pull_request + push)."
                  className="py-12"
                />
              </Surface>
            ) : previewsLoading && previews.length === 0 ? (
              <div className="space-y-3">
                <Skeleton className="h-28 rounded-xl" />
                <Skeleton className="h-28 rounded-xl" />
              </div>
            ) : previews.length === 0 ? (
              <Surface>
                <EmptyState
                  icon={<GitPullRequestIcon className="size-8" />}
                  title="No preview environments yet"
                  description="Open or update a pull request on the linked repo, or confirm Settings → Preview base domain and wildcard DNS."
                  className="py-12"
                />
              </Surface>
            ) : (
              <div className="space-y-4">
                {previews.map((pv) => (
                  <Surface key={pv.id}>
                    <SurfaceHeader>
                      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                        <div className="space-y-2">
                          <SurfaceTitle className="flex flex-wrap items-center gap-2">
                            <span>
                              PR #{pv.pr_number}{" "}
                              <span className="text-muted-foreground font-normal text-sm font-mono">
                                {pv.head_branch}:{pv.head_sha ? pv.head_sha.slice(0, 7) : "—"}
                              </span>
                            </span>
                            <Badge variant={previewLifecycleVariant(pv.lifecycle_state)}>
                              {previewLifecycleLabel(pv.lifecycle_state)}
                            </Badge>
                          </SurfaceTitle>
                          <SurfaceDescription className="flex flex-wrap gap-x-4 gap-y-1">
                            {pv.closed_at != null && (
                              <span>
                                Closed:{" "}
                                <span className="font-mono text-foreground">{new Date(pv.closed_at).toLocaleString()}</span>
                              </span>
                            )}
                            {pv.expires_at != null && (
                              <span>
                                Cleanup:{" "}
                                <span className="font-mono text-foreground">{new Date(pv.expires_at).toLocaleString()}</span>
                              </span>
                            )}
                            {pv.latest_deployment != null && (
                              <Link
                                to={`/deployments/${pv.latest_deployment.id}`}
                                className="text-primary underline-offset-4 hover:underline"
                              >
                                Latest deployment
                              </Link>
                            )}
                          </SurfaceDescription>
                        </div>
                        <div className="flex flex-wrap gap-2">
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            disabled={pv.lifecycle_state !== "active" || previewActionId === pv.id}
                            onClick={() => void handleRedeployPreview(pv.id)}
                          >
                            <RefreshCwIcon
                              className={`size-4 ${previewActionId === pv.id && previewActionKind === "redeploy" ? "animate-spin" : ""}`}
                            />
                            {previewActionId === pv.id && previewActionKind === "redeploy" ? "Redeploying…" : "Redeploy"}
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="destructive"
                            disabled={previewActionId === pv.id}
                            onClick={() => void handleDeletePreview(pv.id)}
                          >
                            <TrashIcon className="size-4" />
                            {previewActionId === pv.id && previewActionKind === "delete" ? "Deleting…" : "Delete now"}
                          </Button>
                        </div>
                      </div>
                    </SurfaceHeader>
                    <SurfaceBody className="space-y-4 text-sm">
                      {pv.latest_deployment != null && (
                        <div className="space-y-2">
                          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Latest deployment</p>
                          <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/20 px-3 py-2">
                            <Badge variant={phaseVariant(pv.latest_deployment.phase)}>{phaseLabel(pv.latest_deployment.phase)}</Badge>
                            <span className="font-mono text-xs sm:text-sm">
                              {pv.latest_deployment.github_commit ? pv.latest_deployment.github_commit.slice(0, 7) : "manual"}
                            </span>
                            {pv.latest_deployment.build_status != null && pv.latest_deployment.build_status !== "" && (
                              <span className="text-xs text-muted-foreground">Build: {pv.latest_deployment.build_status}</span>
                            )}
                            {pv.latest_deployment.preview_scaled_to_zero && (
                              <Badge variant="outline">Scaled to zero</Badge>
                            )}
                            {pv.latest_deployment.wake_requested_at != null && (
                              <Badge variant="secondary">Waking</Badge>
                            )}
                            {pv.latest_deployment.created_at != null && (
                              <span className="text-xs text-muted-foreground">
                                Created {new Date(pv.latest_deployment.created_at).toLocaleString()}
                              </span>
                            )}
                          </div>
                        </div>
                      )}
                      {pv.lifecycle_state !== "active" && (
                        <div className="rounded-md border border-dashed bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
                          This preview has been closed. URLs remain listed until cleanup, but they no longer accept
                          traffic or cold-start the app.
                        </div>
                      )}
                      {pv.stable_url != null && pv.stable_url !== "" && (
                        <div>
                          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-1">
                            {pv.lifecycle_state === "active" ? "Stable URL" : "Stable URL (historical)"}
                          </p>
                          <div className="flex flex-wrap items-center gap-2">
                            {pv.lifecycle_state === "active" ? (
                              <a
                                href={pv.stable_url}
                                target="_blank"
                                rel="noreferrer"
                                className="font-mono text-primary break-all underline-offset-4 hover:underline"
                              >
                                {pv.stable_url}
                              </a>
                            ) : (
                              <span className="font-mono break-all text-muted-foreground">{pv.stable_url}</span>
                            )}
                            <Button
                              type="button"
                              size="icon"
                              variant="ghost"
                              className="h-8 w-8 shrink-0"
                              aria-label="Copy stable URL"
                              onClick={() => void copyText("stable", pv.stable_url ?? "")}
                            >
                              <CopyIcon className="size-4" />
                            </Button>
                          </div>
                        </div>
                      )}
                      {pv.immutable_urls != null && pv.immutable_urls.length > 0 && (
                        <div>
                          <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                            {pv.lifecycle_state === "active" ? "Immutable URLs" : "Immutable URLs (historical)"}
                          </p>
                          <ul className="space-y-2">
                            {pv.immutable_urls.map((row) => (
                              <li key={`${row.url}-${row.deployment_id ?? ""}`} className="flex flex-wrap items-center gap-2 border rounded-md px-3 py-2 bg-muted/20">
                                {pv.lifecycle_state === "active" ? (
                                  <a
                                    href={row.url}
                                    target="_blank"
                                    rel="noreferrer"
                                    className="font-mono text-primary break-all text-xs sm:text-sm underline-offset-4 hover:underline min-w-0 flex-1"
                                  >
                                    {row.url}
                                  </a>
                                ) : (
                                  <span className="font-mono break-all text-xs sm:text-sm text-muted-foreground min-w-0 flex-1">
                                    {row.url}
                                  </span>
                                )}
                                <Button
                                  type="button"
                                  size="icon"
                                  variant="ghost"
                                  className="h-8 w-8 shrink-0"
                                  aria-label="Copy URL"
                                  onClick={() => void copyText("immutable", row.url)}
                                >
                                  <CopyIcon className="size-4" />
                                </Button>
                                {row.github_commit != null && row.github_commit !== "" && (
                                  <span className="text-xs text-muted-foreground font-mono shrink-0">
                                    {row.github_commit.slice(0, 7)}
                                  </span>
                                )}
                                {row.deployment_id != null && (
                                  <Link
                                    to={`/deployments/${row.deployment_id}`}
                                    className="text-xs text-primary underline-offset-4 hover:underline shrink-0"
                                  >
                                    Deployment
                                  </Link>
                                )}
                              </li>
                            ))}
                          </ul>
                        </div>
                      )}
                    </SurfaceBody>
                  </Surface>
                ))}
              </div>
            )}
          </TabsContent>

          {/* ── Usage ───────────────────────────────────────── */}
          <TabsContent value="usage" className="space-y-4 min-w-0">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-sm text-muted-foreground max-w-xl">
                CPU, memory, and disk from workload instances; HTTP requests and edge traffic require TLS edge on :443.
              </p>
              <div className="flex flex-wrap items-center gap-2">
                {(["1h", "24h", "7d"] as const).map((w) => (
                  <Button key={w} type="button" size="sm" variant={usageWindow === w ? "default" : "outline"} onClick={() => setUsageWindow(w)}>
                    {w}
                  </Button>
                ))}
                <Button type="button" size="sm" variant="secondary" onClick={() => void loadUsage()} disabled={usageLoading}>
                  <RefreshCwIcon className={`mr-2 size-4 ${usageLoading ? "animate-spin" : ""}`} /> Refresh
                </Button>
              </div>
            </div>

            {usageLoading && !usageCurrent ? (
              <div className="space-y-3">
                <Skeleton className="h-24 rounded-xl" />
                <Skeleton className="h-40 rounded-xl" />
              </div>
            ) : usageCurrent ? (
              <>
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <div className="stat-card">
                    <p className="stat-label">Avg CPU (instances)</p>
                    <p className="stat-value mt-2">
                      {usageCurrent.summary?.cpu_percent_avg != null
                        ? `${usageCurrent.summary.cpu_percent_avg.toFixed(1)}%`
                        : "—"}
                    </p>
                  </div>
                  <div className="stat-card">
                    <p className="stat-label">Memory (RSS total)</p>
                    <p className="stat-value mt-2">
                      {formatBytes(usageCurrent.summary?.memory_rss_bytes_total ?? 0)}
                    </p>
                  </div>
                  <div className="stat-card space-y-1.5">
                    <p className="stat-label">HTTP (15m, edge)</p>
                    <p className="text-sm mt-2">
                      <span className="text-muted-foreground">Requests:</span>{" "}
                      <span className="font-mono">{usageCurrent.summary?.http_requests_15m ?? 0}</span>
                    </p>
                    <p className="text-xs text-muted-foreground">
                      2xx/3xx: <span className="font-mono text-foreground">{usageCurrent.summary?.http_status_2xx_15m ?? 0}</span>{" "}
                      · 4xx: <span className="font-mono text-foreground">{usageCurrent.summary?.http_status_4xx_15m ?? 0}</span>{" "}
                      · 5xx: <span className="font-mono text-foreground">{usageCurrent.summary?.http_status_5xx_15m ?? 0}</span>
                    </p>
                    <p className="text-xs">
                      In: {formatBytes(usageCurrent.summary?.http_bytes_in_15m ?? 0)} · Out:{" "}
                      {formatBytes(usageCurrent.summary?.http_bytes_out_15m ?? 0)}
                    </p>
                  </div>
                </div>

                {(usageCurrent.instances ?? []).length > 0 ? (
                  <Surface>
                    <SurfaceHeader>
                      <SurfaceTitle>Instances</SurfaceTitle>
                      <SurfaceDescription>Latest sample per replica (last ~2h).</SurfaceDescription>
                    </SurfaceHeader>
                    <SurfaceBody className="overflow-x-auto">
                      <table className="w-full text-sm">
                        <thead>
                          <tr className="border-b text-left text-muted-foreground">
                            <th className="py-2 pr-4 font-medium text-xs">Instance</th>
                            <th className="py-2 pr-4 font-medium text-xs">Runtime</th>
                            <th className="py-2 pr-4 font-medium text-xs">CPU %</th>
                            <th className="py-2 pr-4 font-medium text-xs">RAM</th>
                            <th className="py-2 pr-4 font-medium text-xs">Disk R/W</th>
                          </tr>
                        </thead>
                        <tbody>
                          {(usageCurrent.instances ?? []).map((row) => (
                            <tr key={row.deployment_instance_id} className="border-b border-border/60">
                              <td className="py-2.5 pr-4 font-mono text-xs">{row.deployment_instance_id.slice(0, 8)}…</td>
                              <td className="py-2.5 pr-4 text-sm">{row.source}</td>
                              <td className="py-2.5 pr-4 tabular-nums text-sm">{row.cpu_percent != null ? `${row.cpu_percent.toFixed(1)}%` : "—"}</td>
                              <td className="py-2.5 pr-4 tabular-nums text-sm">{formatBytes(row.memory_rss_bytes)}</td>
                              <td className="py-2.5 pr-4 text-xs tabular-nums text-muted-foreground">
                                {formatBytes(row.disk_read_bytes)} / {formatBytes(row.disk_write_bytes)}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </SurfaceBody>
                  </Surface>
                ) : (
                  <p className="text-sm text-muted-foreground">
                    No usage samples yet. Start a deployment on this server; metrics collect every ~15s.
                  </p>
                )}

                {usageHistory && (
                  <div className="grid gap-4 lg:grid-cols-2">
                    <Surface>
                      <SurfaceHeader>
                        <SurfaceTitle>Memory (max / minute)</SurfaceTitle>
                        <SurfaceDescription>Window: {usageHistory.window}</SurfaceDescription>
                      </SurfaceHeader>
                      <SurfaceBody>
                        <MiniBars label="RSS peak per bucket" values={(usageHistory.resource ?? []).map((x) => x.memory_rss_bytes_max)} />
                      </SurfaceBody>
                    </Surface>
                    <Surface>
                      <SurfaceHeader>
                        <SurfaceTitle>HTTP requests / minute</SurfaceTitle>
                        <SurfaceDescription>Aggregated across Kindling edge servers</SurfaceDescription>
                      </SurfaceHeader>
                      <SurfaceBody>
                        <MiniBars label="Requests" values={(usageHistory.http ?? []).map((h) => h.request_count)} />
                      </SurfaceBody>
                    </Surface>
                  </div>
                )}
              </>
            ) : (
              <p className="text-sm text-muted-foreground">Open this tab to load usage.</p>
            )}
          </TabsContent>
        </Tabs>
      </PageSection>

      {/* ── Dialogs ──────────────────────────────────────── */}
      <Dialog open={deployDialogOpen} onOpenChange={setDeployDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deploy</DialogTitle>
            <DialogDescription>Trigger a new deployment for {project.name}.</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
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
            </div>
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
            <Button variant="outline" onClick={() => setDeployDialogOpen(false)}>Cancel</Button>
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
              Are you sure you want to delete <strong>{project.name}</strong>? This will remove all deployments and cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteDialogOpen(false)}>Cancel</Button>
            <Button variant="destructive" onClick={() => void handleDelete()} disabled={deleting}>
              {deleting ? "Deleting..." : "Delete Project"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageContainer>
  )
}
