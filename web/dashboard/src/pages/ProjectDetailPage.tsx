import { useEffect, useState, useCallback, useMemo } from "react"
import { useParams, Link, useNavigate } from "react-router-dom"
import {
  api,
  type Project,
  type Deployment,
  type CIJob,
  type CIWorkflow,
  type PreviewEnvironment,
  type GitHubSetup,
  type GitHead,
  type ProjectDomain,
  type ProjectVolume,
  type ProjectVolumeBackup,
  type ProjectSecret,
  type Server,
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
  GitBranchIcon,
  GitPullRequestIcon,
  KeyRoundIcon,
  HardDriveIcon,
  PlayIcon,
} from "lucide-react"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import { deploymentPromotionConfirmMessage } from "@/lib/deployment-promotion"
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

function defaultCITriggerEvent(workflow: CIWorkflow): string {
  if (workflow.triggers?.workflow_dispatch) return "workflow_dispatch"
  if (workflow.triggers?.push) return "push"
  if (workflow.triggers?.pull_request) return "pull_request"
  return "workflow_dispatch"
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

  const [minInstances, setMinInstances] = useState(0)
  const [maxInstances, setMaxInstances] = useState(3)
  const [scaleToZeroEnabled, setScaleToZeroEnabled] = useState(true)
  const [scalingSaving, setScalingSaving] = useState(false)
  const [buildRuleSaving, setBuildRuleSaving] = useState(false)
  const [buildRootDirectory, setBuildRootDirectory] = useState("/")
  const [buildDockerfilePath, setBuildDockerfilePath] = useState("Dockerfile")
  const [buildPathsSaving, setBuildPathsSaving] = useState(false)

  const [domains, setDomains] = useState<ProjectDomain[]>([])
  const [domainsLoading, setDomainsLoading] = useState(false)
  const [newDomainName, setNewDomainName] = useState("")
  const [domainSaving, setDomainSaving] = useState(false)
  const [verifyingId, setVerifyingId] = useState<string | null>(null)
  const [projectVolume, setProjectVolume] = useState<ProjectVolume | null>(null)
  const [volumeBackups, setVolumeBackups] = useState<ProjectVolumeBackup[]>([])
  const [volumeMountPath, setVolumeMountPath] = useState("/data")
  const [volumeSizeGB, setVolumeSizeGB] = useState(5)
  const [volumeBackupSchedule, setVolumeBackupSchedule] = useState<"off" | "manual" | "daily" | "weekly">("manual")
  const [volumeBackupRetentionCount, setVolumeBackupRetentionCount] = useState(7)
  const [volumePreDeleteBackupEnabled, setVolumePreDeleteBackupEnabled] = useState(false)
  const [volumeTargetServerID, setVolumeTargetServerID] = useState("")
  const [volumeSaving, setVolumeSaving] = useState(false)
  const [volumeDeleting, setVolumeDeleting] = useState(false)
  const [volumeActionKind, setVolumeActionKind] = useState<"backup" | "restore" | "move" | "repair" | null>(null)
  const [volumeActionBackupID, setVolumeActionBackupID] = useState<string | null>(null)
  const [servers, setServers] = useState<Server[]>([])
  const [secrets, setSecrets] = useState<ProjectSecret[]>([])
  const [secretsLoading, setSecretsLoading] = useState(false)
  const [secretName, setSecretName] = useState("")
  const [secretValue, setSecretValue] = useState("")
  const [secretSaving, setSecretSaving] = useState(false)
  const [deletingSecretId, setDeletingSecretId] = useState<string | null>(null)
  const [ciJobs, setCIJobs] = useState<CIJob[]>([])
  const [ciJobsLoading, setCIJobsLoading] = useState(false)
  const [ciWorkflows, setCIWorkflows] = useState<CIWorkflow[]>([])
  const [ciWorkflowsLoading, setCIWorkflowsLoading] = useState(false)
  const [ciCancelingId, setCICancelingId] = useState<string | null>(null)
  const [ciSelectedWorkflowStem, setCISelectedWorkflowStem] = useState("")
  const [ciCatalogTab, setCICatalogTab] = useState<"overview" | "dispatch">("overview")
  const [ciTriggerDialogOpen, setCITriggerDialogOpen] = useState(false)
  const [ciTriggerWorkflowStem, setCITriggerWorkflowStem] = useState("")
  const [ciTriggerJobID, setCITriggerJobID] = useState("")
  const [ciTriggerInputs, setCITriggerInputs] = useState<Record<string, string>>({})
  const [ciTriggerSubmitting, setCITriggerSubmitting] = useState(false)

  const [mainTab, setMainTab] = useState("overview")
  const [usageCurrent, setUsageCurrent] = useState<UsageCurrent | null>(null)
  const [usageHistory, setUsageHistory] = useState<UsageHistory | null>(null)
  const [usageLoading, setUsageLoading] = useState(false)
  const [usageWindow, setUsageWindow] = useState<"1h" | "24h" | "7d">("24h")

  const [previews, setPreviews] = useState<PreviewEnvironment[]>([])
  const [previewsLoading, setPreviewsLoading] = useState(false)
  const [deploymentActionId, setDeploymentActionId] = useState<string | null>(null)
  const [previewActionId, setPreviewActionId] = useState<string | null>(null)
  const [previewActionKind, setPreviewActionKind] = useState<"redeploy" | "delete" | null>(null)

  const hasGitHubRepo = Boolean(project?.github_repository?.trim())
  const selectedCIWorkflow = useMemo(
    () => ciWorkflows.find((workflow) => workflow.stem === ciSelectedWorkflowStem) ?? ciWorkflows[0] ?? null,
    [ciSelectedWorkflowStem, ciWorkflows],
  )
  const ciTriggerWorkflow = useMemo(
    () => ciWorkflows.find((workflow) => workflow.stem === ciTriggerWorkflowStem) ?? null,
    [ciTriggerWorkflowStem, ciWorkflows],
  )

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

  const loadCIJobs = useCallback(async (projectId: string) => {
    setCIJobsLoading(true)
    try {
      const list = await api.listProjectCIJobs(projectId)
      setCIJobs(list)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load CI jobs")
      setCIJobs([])
    } finally {
      setCIJobsLoading(false)
    }
  }, [])

  const loadCIWorkflows = useCallback(async (projectId: string) => {
    setCIWorkflowsLoading(true)
    try {
      const list = await api.listProjectCIWorkflows(projectId, "main")
      setCIWorkflows(list)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load workflows")
      setCIWorkflows([])
    } finally {
      setCIWorkflowsLoading(false)
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
    if (!id || mainTab !== "ci") return
    void loadCIJobs(id)
    if (hasGitHubRepo) {
      void loadCIWorkflows(id)
    } else {
      setCIWorkflows([])
    }
  }, [hasGitHubRepo, id, mainTab, loadCIJobs, loadCIWorkflows])

  useEffect(() => {
    if (!id || mainTab !== "ci") return
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void loadCIJobs(id)
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.project(id), dashboardEventTopics.projectCIJobs(id)],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [id, mainTab, loadCIJobs])

  useEffect(() => {
    if (ciWorkflows.length === 0) {
      setCISelectedWorkflowStem("")
      return
    }
    if (!ciWorkflows.some((workflow) => workflow.stem === ciSelectedWorkflowStem)) {
      setCISelectedWorkflowStem(ciWorkflows[0].stem)
    }
  }, [ciSelectedWorkflowStem, ciWorkflows])

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
        api.listProjectVolumeBackups(id).catch((e) => {
          if (e instanceof APIError && e.status === 404) {
            return []
          }
          throw e
        }),
        api.listServers().catch(() => []),
      ])
        .then(([p, d, v, backups, serverList]) => {
          setProject(p)
          setDeployments(d)
          setProjectVolume(v)
          setVolumeBackups(backups)
          setServers(serverList)
          setVolumeMountPath(v?.mount_path || "/data")
          setVolumeSizeGB(v?.size_gb ?? 5)
          setVolumeBackupSchedule(v?.backup_policy?.schedule ?? "manual")
          setVolumeBackupRetentionCount(v?.backup_policy?.retention_count ?? 7)
          setVolumePreDeleteBackupEnabled(Boolean(v?.backup_policy?.pre_delete_backup_enabled))
          setVolumeTargetServerID(v?.server_id || "")
          setMinInstances(typeof p.min_instance_count === "number" && p.min_instance_count >= 0 ? p.min_instance_count : 0)
          setMaxInstances(typeof p.max_instance_count === "number" && p.max_instance_count >= 0 ? p.max_instance_count : 3)
          setScaleToZeroEnabled(p.scale_to_zero_enabled !== false)
          setBuildRootDirectory(p.root_directory?.trim() ? p.root_directory : "/")
          setBuildDockerfilePath(p.dockerfile_path?.trim() ? p.dockerfile_path : "Dockerfile")
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
    setMinInstances(0)
    setMaxInstances(3)
    setScaleToZeroEnabled(true)
    setBuildRootDirectory("/")
    setBuildDockerfilePath("Dockerfile")
    setProjectVolume(null)
    setVolumeBackups([])
    setVolumeMountPath("/data")
    setVolumeSizeGB(5)
    setVolumeBackupSchedule("manual")
    setVolumeBackupRetentionCount(7)
    setVolumePreDeleteBackupEnabled(false)
    setVolumeTargetServerID("")
    setSecrets([])
    setSecretName("")
    setSecretValue("")
    setCIJobs([])
    setCIWorkflows([])
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

  const handlePromoteDeployment = async (deployment: Deployment) => {
    if (!deployment.can_promote_to_production) return
    if (!window.confirm(deploymentPromotionConfirmMessage(deployment))) return
    setDeploymentActionId(deployment.id)
    setError(null)
    try {
      const next = await api.promoteDeployment(deployment.id)
      navigate(`/deployments/${next.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not promote deployment")
    } finally {
      setDeploymentActionId(null)
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
    const min = Math.max(0, Math.floor(Number(minInstances)) || 0)
    const max = Math.max(0, Math.floor(Number(maxInstances)) || 0)
    setScalingSaving(true)
    setError(null)
    try {
      const p = await api.patchProject(id, {
        min_instance_count: min,
        max_instance_count: max,
        scale_to_zero_enabled: scaleToZeroEnabled,
      })
      setProject(p)
      setMinInstances(p.min_instance_count ?? min)
      setMaxInstances(p.max_instance_count ?? max)
      setScaleToZeroEnabled(p.scale_to_zero_enabled !== false)
      const d = await api.listDeployments(id)
      setDeployments(d)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not update scaling")
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

  const handleSaveBuildPaths = async () => {
    if (!id) return
    const root = buildRootDirectory.trim() || "/"
    const dockerfile = buildDockerfilePath.trim() || "Dockerfile"
    setBuildPathsSaving(true)
    setError(null)
    try {
      const p = await api.patchProject(id, { root_directory: root, dockerfile_path: dockerfile })
      setProject(p)
      setBuildRootDirectory(p.root_directory?.trim() ? p.root_directory : "/")
      setBuildDockerfilePath(p.dockerfile_path?.trim() ? p.dockerfile_path : "Dockerfile")
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not update build paths")
    } finally {
      setBuildPathsSaving(false)
    }
  }

  const handleSaveVolume = async () => {
    if (!id) return
    setVolumeSaving(true)
    setError(null)
    try {
      const volume = await api.putProjectVolume(id, {
        mount_path: volumeMountPath.trim() || "/data",
        size_gb: Math.max(1, Math.floor(Number(volumeSizeGB)) || 5),
        backup_schedule: volumeBackupSchedule,
        backup_retention_count: Math.max(1, Math.floor(Number(volumeBackupRetentionCount)) || 7),
        pre_delete_backup_enabled: volumePreDeleteBackupEnabled,
      })
      setProjectVolume(volume)
      setVolumeMountPath(volume.mount_path)
      setVolumeSizeGB(volume.size_gb)
      setVolumeBackupSchedule(volume.backup_policy.schedule)
      setVolumeBackupRetentionCount(volume.backup_policy.retention_count)
      setVolumePreDeleteBackupEnabled(volume.backup_policy.pre_delete_backup_enabled)
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
      await loadProjectPage()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete persistent volume")
    } finally {
      setVolumeDeleting(false)
    }
  }

  const handleCreateVolumeBackup = async () => {
    if (!id || !projectVolume) return
    setVolumeActionKind("backup")
    setVolumeActionBackupID(null)
    setError(null)
    try {
      await api.createProjectVolumeBackup(id)
      await loadProjectPage()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not create a volume backup")
    } finally {
      setVolumeActionKind(null)
    }
  }

  const handleRepairVolume = async () => {
    if (!id || !projectVolume) return
    setVolumeActionKind("repair")
    setVolumeActionBackupID(null)
    setError(null)
    try {
      await api.repairProjectVolume(id)
      await loadProjectPage()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not repair the volume")
    } finally {
      setVolumeActionKind(null)
    }
  }

  const handleMoveVolume = async () => {
    if (!id || !projectVolume) return
    setVolumeActionKind("move")
    setVolumeActionBackupID(null)
    setError(null)
    try {
      await api.moveProjectVolume(id, {
        target_server_id: volumeTargetServerID.trim() || undefined,
      })
      await loadProjectPage()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not move the volume")
    } finally {
      setVolumeActionKind(null)
    }
  }

  const handleRestoreVolume = async (backupId: string) => {
    if (!id || !projectVolume) return
    setVolumeActionKind("restore")
    setVolumeActionBackupID(backupId)
    setError(null)
    try {
      await api.restoreProjectVolume(id, {
        backup_id: backupId,
        target_server_id: volumeTargetServerID.trim() || undefined,
      })
      await loadProjectPage()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not restore the volume from backup")
    } finally {
      setVolumeActionKind(null)
      setVolumeActionBackupID(null)
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

  const handleCancelCIJob = async (jobId: string) => {
    setCICancelingId(jobId)
    try {
      await api.cancelCIJob(jobId)
      if (id) {
        await loadCIJobs(id)
      }
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not cancel CI job")
    } finally {
      setCICancelingId(null)
    }
  }

  const openCITriggerDialog = (workflow: CIWorkflow) => {
    setCITriggerWorkflowStem(workflow.stem)
    setCITriggerJobID(workflow.jobs.length === 1 ? workflow.jobs[0].id : "")
    setCITriggerInputs(workflow.inputs ? { ...workflow.inputs } : {})
    setCITriggerDialogOpen(true)
  }

  const handleRunCIWorkflow = async () => {
    if (!id || !ciTriggerWorkflow) return
    setCITriggerSubmitting(true)
    try {
      const created = await api.createProjectCIJob(id, {
        workflow: ciTriggerWorkflow.stem,
        job: ciTriggerJobID.trim() || undefined,
        event: defaultCITriggerEvent(ciTriggerWorkflow),
        inputs: ciTriggerInputs,
        require_microvm: true,
        ref: "main",
      })
      setCITriggerDialogOpen(false)
      setCITriggerJobID("")
      if (id) {
        await loadCIJobs(id)
      }
      navigate(`/ci/jobs/${created.id}`)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not start CI job")
    } finally {
      setCITriggerSubmitting(false)
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
  const volumeBusy =
    projectVolume?.current_operation != null &&
    (projectVolume.current_operation.status === "pending" || projectVolume.current_operation.status === "running")
  const activeCloudHypervisorServers = servers.filter(
    (server) => server.status === "active" && server.runtime === "cloud-hypervisor",
  )

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
            <TabsTrigger value="volume">
              <HardDriveIcon className="size-4" />
              Volume
            </TabsTrigger>
            <TabsTrigger value="ci">
              <GitBranchIcon className="size-4" />
              CI
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
                </MetadataGrid>
                <div className="rounded-lg border bg-muted/20 p-4 space-y-3">
                  <div>
                    <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Build context</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Paths are relative to the repository root. Saving updates this project and syncs the primary
                      service; trigger a new deploy for changes to take effect.
                    </p>
                  </div>
                  <div className="grid gap-3 md:grid-cols-2">
                    <div className="space-y-1 min-w-0">
                      <Label htmlFor="project-root-dir" className="text-xs text-muted-foreground">
                        Root directory
                      </Label>
                      <Input
                        id="project-root-dir"
                        className="font-mono h-9"
                        value={buildRootDirectory}
                        disabled={buildPathsSaving}
                        onChange={(e) => setBuildRootDirectory(e.target.value)}
                        placeholder="/web/landing"
                      />
                    </div>
                    <div className="space-y-1 min-w-0">
                      <Label htmlFor="project-dockerfile" className="text-xs text-muted-foreground">
                        Dockerfile path
                      </Label>
                      <Input
                        id="project-dockerfile"
                        className="font-mono h-9"
                        value={buildDockerfilePath}
                        disabled={buildPathsSaving}
                        onChange={(e) => setBuildDockerfilePath(e.target.value)}
                        placeholder="Dockerfile"
                      />
                    </div>
                  </div>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-end">
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      disabled={buildPathsSaving}
                      className="shrink-0"
                      onClick={() => void handleSaveBuildPaths()}
                    >
                      {buildPathsSaving ? "Saving…" : "Save build context"}
                    </Button>
                  </div>
                </div>
              </SurfaceBody>
              <SurfaceSeparator />
              <SurfaceBody className="space-y-3">
                <div>
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Horizontal scaling</p>
                  <p className="text-xs text-muted-foreground mt-1">
                    Traffic autoscaling keeps the deployment between the configured minimum and maximum, with an
                    optional idle path down to zero.
                  </p>
                </div>
                {latestRunningDeployment != null && (
                  <div className="space-y-1 text-sm">
                    <p>
                      Current target:{" "}
                      <span className="font-mono">
                        {latestRunningDeployment.desired_instance_count ?? project.desired_instance_count ?? 1}
                      </span>
                    </p>
                    <p>
                      Running now:{" "}
                      <span className="font-mono">{latestRunningDeployment.running_instance_count ?? 0}</span>
                    </p>
                  </div>
                )}
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="space-y-1 min-w-0">
                    <Label className="text-xs text-muted-foreground">Minimum instances</Label>
                    <Input
                      type="number"
                      min={0}
                      className="font-mono h-9 w-full sm:max-w-[120px]"
                      value={minInstances}
                      onChange={(e) => setMinInstances(Math.max(0, Number(e.target.value) || 0))}
                    />
                  </div>
                  <div className="space-y-1 min-w-0">
                    <Label className="text-xs text-muted-foreground">Maximum instances</Label>
                    <Input
                      type="number"
                      min={0}
                      className="font-mono h-9 w-full sm:max-w-[120px]"
                      value={maxInstances}
                      onChange={(e) => setMaxInstances(Math.max(0, Number(e.target.value) || 0))}
                    />
                  </div>
                </div>
                <label className="flex items-start gap-3 rounded-lg border p-3">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4"
                    checked={scaleToZeroEnabled}
                    disabled={scalingSaving}
                    onChange={(e) => setScaleToZeroEnabled(e.target.checked)}
                  />
                  <div className="min-w-0">
                    <p className="text-sm font-medium">Allow scale to zero</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      When enabled, idle production traffic can fully drain the project and the edge will wake it on
                      the next request.
                    </p>
                  </div>
                </label>
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                  <p className="text-xs text-muted-foreground">
                    Range: <span className="font-mono">{project.min_instance_count ?? minInstances}</span> to{" "}
                    <span className="font-mono">{project.max_instance_count ?? maxInstances}</span>
                    {scaleToZeroEnabled ? ", zero enabled" : ", zero disabled"}
                  </p>
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
                  <DeploymentReachability reachable={latestRunningDeployment.reachable} />
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

          {/* ── Persistent volume ───────────────────────────── */}
          <TabsContent value="volume" className="space-y-4 min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Persistent volume</SurfaceTitle>
                <SurfaceDescription>
                  Attach one durable block volume at a mount path like <span className="font-mono">/data</span>.
                  Volume-backed projects run as a single active instance on one Cloud Hypervisor worker, and redeploys may
                  briefly stop the old VM before the new one starts.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-4">
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
                <div className="grid gap-3 md:grid-cols-3">
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">Backup schedule</Label>
                    <select
                      className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                      value={volumeBackupSchedule}
                      onChange={(e) => setVolumeBackupSchedule(e.target.value as "off" | "manual" | "daily" | "weekly")}
                    >
                      <option value="off">Off</option>
                      <option value="manual">Manual</option>
                      <option value="daily">Daily</option>
                      <option value="weekly">Weekly</option>
                    </select>
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs text-muted-foreground">Backup retention</Label>
                    <Input
                      type="number"
                      min={1}
                      className="font-mono h-9"
                      value={volumeBackupRetentionCount}
                      onChange={(e) => setVolumeBackupRetentionCount(Number(e.target.value))}
                    />
                  </div>
                  <label className="flex items-center gap-3 rounded-lg border px-3 py-2 md:mt-6">
                    <input
                      type="checkbox"
                      className="size-4"
                      checked={volumePreDeleteBackupEnabled}
                      onChange={(e) => setVolumePreDeleteBackupEnabled(e.target.checked)}
                    />
                    <span className="text-sm">Backup before delete</span>
                  </label>
                </div>
                {projectVolume ? (
                  <div className="rounded-lg border bg-muted/20 p-3 space-y-3 text-sm">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={projectVolume.status === "attached" ? "default" : "secondary"}>
                        {projectVolume.status}
                      </Badge>
                      <Badge variant={projectVolume.health === "healthy" ? "default" : "secondary"}>
                        {projectVolume.health}
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
                    {projectVolume.last_successful_backup_at ? (
                      <p>
                        Last backup:{" "}
                        <span className="font-mono">{new Date(projectVolume.last_successful_backup_at).toLocaleString()}</span>
                      </p>
                    ) : null}
                    {projectVolume.current_operation ? (
                      <div className="rounded-md border bg-background/70 px-3 py-2">
                        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Current operation</p>
                        <div className="mt-1 flex flex-wrap items-center gap-2">
                          <Badge variant="secondary">{projectVolume.current_operation.kind}</Badge>
                          <Badge variant="outline">{projectVolume.current_operation.status}</Badge>
                          {projectVolume.current_operation.target_server_id ? (
                            <span className="font-mono text-xs">{projectVolume.current_operation.target_server_id}</span>
                          ) : null}
                        </div>
                        {projectVolume.current_operation.error ? (
                          <p className="mt-2 text-xs text-destructive">{projectVolume.current_operation.error}</p>
                        ) : null}
                      </div>
                    ) : null}
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
                    disabled={volumeSaving || volumeBusy}
                    onClick={() => void handleSaveVolume()}
                  >
                    {volumeSaving ? "Saving…" : projectVolume ? "Update volume" : "Enable volume"}
                  </Button>
                  {projectVolume ? (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={volumeDeleting || projectVolume.status === "attached" || volumeBusy}
                      onClick={() => void handleDeleteVolume()}
                    >
                      {volumeDeleting ? "Deleting…" : volumePreDeleteBackupEnabled ? "Delete with backup" : "Delete volume"}
                    </Button>
                  ) : null}
                </div>
                {projectVolume ? (
                  <>
                    <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto_auto_auto] md:items-end">
                      <div className="space-y-1">
                        <Label className="text-xs text-muted-foreground">Target server</Label>
                        <Input
                          list="project-volume-target-servers"
                          className="font-mono h-9"
                          value={volumeTargetServerID}
                          onChange={(e) => setVolumeTargetServerID(e.target.value)}
                          placeholder={projectVolume.server_id || "server UUID"}
                        />
                        <datalist id="project-volume-target-servers">
                          {activeCloudHypervisorServers.map((server) => (
                            <option key={server.id} value={server.id}>
                              {server.hostname}
                            </option>
                          ))}
                        </datalist>
                      </div>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={volumeBusy || volumeActionKind === "backup"}
                        onClick={() => void handleCreateVolumeBackup()}
                      >
                        {volumeActionKind === "backup" ? "Backing up…" : "Create backup"}
                      </Button>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={volumeBusy || volumeActionKind === "move"}
                        onClick={() => void handleMoveVolume()}
                      >
                        {volumeActionKind === "move" ? "Moving…" : "Move detached volume"}
                      </Button>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={volumeBusy || volumeActionKind === "repair"}
                        onClick={() => void handleRepairVolume()}
                      >
                        {volumeActionKind === "repair" ? "Repairing…" : "Run repair"}
                      </Button>
                    </div>
                    <div className="space-y-2">
                      <div className="flex items-center justify-between gap-3">
                        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Backup history</p>
                        <p className="text-xs text-muted-foreground">
                          Cold backups only. The volume must stay detached while backup, restore, move, or repair runs.
                        </p>
                      </div>
                      {volumeBackups.length === 0 ? (
                        <p className="text-xs text-muted-foreground">No backups recorded yet.</p>
                      ) : (
                        <div className="space-y-2">
                          {volumeBackups.map((backup) => (
                            <div key={backup.id} className="rounded-lg border bg-background/60 px-3 py-2">
                              <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                                <div className="min-w-0">
                                  <div className="flex flex-wrap items-center gap-2">
                                    <Badge variant="secondary">{backup.kind}</Badge>
                                    <Badge variant={backup.status === "succeeded" ? "default" : "outline"}>
                                      {backup.status}
                                    </Badge>
                                    <span className="text-xs text-muted-foreground">{formatBytes(backup.size_bytes)}</span>
                                  </div>
                                  <p className="mt-1 text-xs text-muted-foreground">
                                    {backup.completed_at
                                      ? `Completed ${new Date(backup.completed_at).toLocaleString()}`
                                      : backup.created_at
                                        ? `Created ${new Date(backup.created_at).toLocaleString()}`
                                        : "Pending"}
                                  </p>
                                  {backup.error ? <p className="mt-1 text-xs text-destructive">{backup.error}</p> : null}
                                </div>
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="outline"
                                  disabled={backup.status !== "succeeded" || volumeBusy || volumeActionKind === "restore"}
                                  onClick={() => void handleRestoreVolume(backup.id)}
                                >
                                  {volumeActionKind === "restore" && volumeActionBackupID === backup.id ? "Restoring…" : "Restore"}
                                </Button>
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  </>
                ) : null}
              </SurfaceBody>
            </Surface>
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
                      <div className="flex items-center gap-2 pr-3">
                        <Link to={`/deployments/${dep.id}`} className="list-row group min-w-0 flex-1 pr-0">
                          <div className="flex min-w-0 flex-col gap-1">
                            <div className="flex flex-wrap items-center gap-2 min-w-0">
                              <Badge variant={phaseVariant(dep.phase)}>{phaseLabel(dep.phase)}</Badge>
                              <span className="font-mono text-sm">{dep.github_commit ? dep.github_commit.slice(0, 8) : "manual"}</span>
                              {dep.build_status ? <span className="hidden text-xs text-muted-foreground sm:inline">Build: {dep.build_status}</span> : null}
                            </div>
                            {dep.promoted_from_deployment_id ? (
                              <span className="font-mono text-xs text-muted-foreground">
                                Rollback of {dep.promoted_from_deployment_id.slice(0, 8)}
                              </span>
                            ) : null}
                          </div>
                          <div className="flex items-center gap-2">
                            <span className="text-xs text-muted-foreground shrink-0">{dep.created_at ? new Date(dep.created_at).toLocaleString() : ""}</span>
                            <ChevronRightIcon className="size-4 text-muted-foreground/40 shrink-0 transition-transform group-hover:translate-x-0.5" />
                          </div>
                        </Link>
                        {dep.can_promote_to_production ? (
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            disabled={deploymentActionId === dep.id}
                            onClick={() => void handlePromoteDeployment(dep)}
                          >
                            {deploymentActionId === dep.id ? "Promoting…" : "Promote"}
                          </Button>
                        ) : null}
                      </div>
                    </li>
                  ))}
                </ul>
              </Surface>
            )}
          </TabsContent>

          {/* ── CI ─────────────────────────────────────────── */}
          <TabsContent value="ci" className="space-y-4 min-w-0">
            <div className="grid gap-4 xl:grid-cols-[minmax(0,1.1fr)_minmax(0,0.9fr)]">
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Recent runs</SurfaceTitle>
                  <SurfaceDescription>
                    Workflow-native CI runs for this project, including execution backend, logs, and artifacts.
                  </SurfaceDescription>
                </SurfaceHeader>
                {ciJobsLoading && ciJobs.length === 0 ? (
                  <SurfaceBody className="space-y-3">
                    <Skeleton className="h-20 rounded-xl" />
                    <Skeleton className="h-20 rounded-xl" />
                  </SurfaceBody>
                ) : ciJobs.length === 0 ? (
                  <SurfaceBody>
                    <EmptyState
                      icon={<GitBranchIcon className="size-8" />}
                      title="No CI jobs yet"
                      description="Pick a workflow on the right and run it from the latest snapshot on main."
                      action={
                        selectedCIWorkflow && hasGitHubRepo ? (
                          <Button size="sm" onClick={() => openCITriggerDialog(selectedCIWorkflow)}>
                            <PlayIcon className="mr-2 size-4" />
                            Run selected workflow
                          </Button>
                        ) : null
                      }
                      className="py-12"
                    />
                  </SurfaceBody>
                ) : (
                  <SurfaceBody className="p-0">
                    <ul className="divide-y">
                      {ciJobs.map((job) => (
                        <li key={job.id}>
                          <div className="flex items-center gap-2 pr-3">
                            <Link to={`/ci/jobs/${job.id}`} className="list-row group min-w-0 flex-1 pr-0">
                              <div className="flex min-w-0 flex-col gap-1.5">
                                <div className="flex flex-wrap items-center gap-2 min-w-0">
                                  <Badge variant={ciStatusVariant(job.status)}>{ciStatusLabel(job.status)}</Badge>
                                  <span className="font-medium truncate">{job.workflow_name || "Workflow"}</span>
                                  {job.selected_job_id ? (
                                    <span className="font-mono text-xs text-muted-foreground">{job.selected_job_id}</span>
                                  ) : null}
                                </div>
                                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                  <span>{job.execution_backend || "pending backend"}</span>
                                  <span>•</span>
                                  <span>{job.require_microvm ? "MicroVM required" : "Host fallback allowed"}</span>
                                  {job.source === "github_actions_runner" && job.external_repo ? (
                                    <>
                                      <span>•</span>
                                      <span>{job.external_repo}</span>
                                    </>
                                  ) : null}
                                  {job.runner_name ? (
                                    <>
                                      <span>•</span>
                                      <span className="font-mono">{job.runner_name}</span>
                                    </>
                                  ) : null}
                                  {job.exit_code != null ? (
                                    <>
                                      <span>•</span>
                                      <span>Exit {job.exit_code}</span>
                                    </>
                                  ) : null}
                                </div>
                              </div>
                              <div className="flex items-center gap-2">
                                <span className="text-xs text-muted-foreground shrink-0">
                                  {job.created_at ? new Date(job.created_at).toLocaleString() : ""}
                                </span>
                                <ChevronRightIcon className="size-4 text-muted-foreground/40 shrink-0 transition-transform group-hover:translate-x-0.5" />
                              </div>
                            </Link>
                            {!isTerminalCIJob(job.status) ? (
                              <Button
                                type="button"
                                size="sm"
                                variant="outline"
                                disabled={ciCancelingId === job.id}
                                onClick={() => void handleCancelCIJob(job.id)}
                              >
                                {ciCancelingId === job.id ? "Canceling…" : "Cancel"}
                              </Button>
                            ) : null}
                          </div>
                        </li>
                      ))}
                    </ul>
                  </SurfaceBody>
                )}
              </Surface>

              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Workflow catalog</SurfaceTitle>
                  <SurfaceDescription>
                    Workflows discovered from <span className="font-mono">{project.github_repository || "main"}</span> on the
                    <span className="font-mono"> main</span> branch.
                  </SurfaceDescription>
                </SurfaceHeader>
                {!hasGitHubRepo ? (
                  <SurfaceBody>
                    <EmptyState
                      icon={<FolderGitIcon className="size-8" />}
                      title="Connect a GitHub repository first"
                      description="The CI panel can only discover workflows and run jobs once this project has a linked GitHub repository."
                      className="py-12"
                    />
                  </SurfaceBody>
                ) : ciWorkflowsLoading && ciWorkflows.length === 0 ? (
                  <SurfaceBody className="space-y-3">
                    <Skeleton className="h-24 rounded-xl" />
                    <Skeleton className="h-48 rounded-xl" />
                  </SurfaceBody>
                ) : ciWorkflows.length === 0 ? (
                  <SurfaceBody>
                    <EmptyState
                      icon={<GitBranchIcon className="size-8" />}
                      title="No workflows found on main"
                      description="Kindling could not find any files under .github/workflows in the linked repository."
                      className="py-12"
                    />
                  </SurfaceBody>
                ) : (
                  <SurfaceBody className="p-0">
                    <div className="grid md:grid-cols-[220px_minmax(0,1fr)]">
                      <div className="border-b md:border-b-0 md:border-r">
                        <div className="p-2 space-y-1">
                          {ciWorkflows.map((workflow) => (
                            <button
                              key={workflow.stem}
                              type="button"
                              onClick={() => setCISelectedWorkflowStem(workflow.stem)}
                              className={`w-full rounded-lg px-3 py-2 text-left transition-colors ${
                                selectedCIWorkflow?.stem === workflow.stem
                                  ? "bg-accent text-foreground"
                                  : "hover:bg-accent/60 text-muted-foreground"
                              }`}
                            >
                              <div className="flex items-center gap-2">
                                <span className="truncate font-medium text-sm text-foreground">
                                  {workflow.name || workflow.stem}
                                </span>
                                <Badge variant="secondary" className="font-mono shrink-0">
                                  {workflow.stem}
                                </Badge>
                              </div>
                              <p className="mt-1 text-xs text-muted-foreground">{workflow.jobs.length} jobs</p>
                            </button>
                          ))}
                        </div>
                      </div>

                      <div className="p-4">
                        {selectedCIWorkflow ? (
                          <Tabs
                            value={ciCatalogTab}
                            onValueChange={(value) =>
                              setCICatalogTab(value === "dispatch" ? "dispatch" : "overview")
                            }
                            className="space-y-4"
                          >
                            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                              <div className="min-w-0">
                                <div className="flex flex-wrap items-center gap-2">
                                  <p className="font-medium">{selectedCIWorkflow.name || selectedCIWorkflow.stem}</p>
                                  <Badge variant="secondary" className="font-mono">
                                    {selectedCIWorkflow.stem}
                                  </Badge>
                                </div>
                                <p className="font-mono text-xs text-muted-foreground break-all mt-1">
                                  {selectedCIWorkflow.file}
                                </p>
                              </div>
                              <Button size="sm" className="shrink-0" onClick={() => openCITriggerDialog(selectedCIWorkflow)}>
                                <PlayIcon className="mr-2 size-4" />
                                Run on main
                              </Button>
                            </div>

                            <TabsList variant="line" className="w-full justify-start overflow-x-auto">
                              <TabsTrigger value="overview" className="shrink-0">
                                Overview
                              </TabsTrigger>
                              <TabsTrigger value="dispatch" className="shrink-0">
                                Dispatch
                              </TabsTrigger>
                            </TabsList>

                            <TabsContent value="overview" className="space-y-4">
                              <div className="space-y-2">
                                <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Triggers</p>
                                <div className="flex flex-wrap gap-2">
                                  {Object.entries(selectedCIWorkflow.triggers || {})
                                    .filter(([, enabled]) => enabled)
                                    .map(([trigger]) => (
                                      <Badge key={trigger} variant="outline">
                                        {trigger}
                                      </Badge>
                                    ))}
                                </div>
                              </div>
                              <div className="space-y-2">
                                <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Jobs</p>
                                <div className="flex flex-wrap gap-2">
                                  {selectedCIWorkflow.jobs.map((job) => (
                                    <span key={job.id} className="rounded-md bg-muted px-2 py-1 font-mono text-xs">
                                      {job.id}
                                    </span>
                                  ))}
                                </div>
                              </div>
                            </TabsContent>

                            <TabsContent value="dispatch" className="space-y-4">
                              <div className="rounded-lg border bg-muted/20 p-3">
                                <p className="text-sm font-medium">Manual runs currently use GitHub ref <span className="font-mono">main</span>.</p>
                                <p className="text-xs text-muted-foreground mt-1">
                                  Kindling downloads the latest repository snapshot from main, then executes the selected workflow or job in a microVM-backed CI run.
                                </p>
                              </div>
                              {Object.keys(selectedCIWorkflow.inputs || {}).length > 0 ? (
                                <div className="space-y-2">
                                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Dispatch inputs</p>
                                  <div className="space-y-2">
                                    {Object.entries(selectedCIWorkflow.inputs || {}).map(([name, value]) => (
                                      <div key={name} className="rounded-lg border px-3 py-2">
                                        <p className="font-mono text-xs">{name}</p>
                                        <p className="text-xs text-muted-foreground mt-1">
                                          Default: <span className="font-mono">{value || "empty"}</span>
                                        </p>
                                      </div>
                                    ))}
                                  </div>
                                </div>
                              ) : (
                                <p className="text-sm text-muted-foreground">No workflow_dispatch inputs are defined for this workflow.</p>
                              )}
                            </TabsContent>
                          </Tabs>
                        ) : null}
                      </div>
                    </div>
                  </SurfaceBody>
                )}
              </Surface>
            </div>
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

      <Dialog open={ciTriggerDialogOpen} onOpenChange={setCITriggerDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Run workflow</DialogTitle>
            <DialogDescription>
              Trigger a CI run for {project.name} from the latest snapshot on <span className="font-mono">main</span>.
            </DialogDescription>
          </DialogHeader>
          {ciTriggerWorkflow ? (
            <div className="space-y-4">
              <div className="rounded-lg border bg-muted/20 p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="font-medium">{ciTriggerWorkflow.name || ciTriggerWorkflow.stem}</p>
                  <Badge variant="secondary" className="font-mono">
                    {ciTriggerWorkflow.stem}
                  </Badge>
                </div>
                <p className="text-xs text-muted-foreground mt-1">
                  Event: <span className="font-mono">{defaultCITriggerEvent(ciTriggerWorkflow)}</span>
                </p>
              </div>

              <div className="space-y-2">
                <Label htmlFor="ci-job-select">Job</Label>
                <select
                  id="ci-job-select"
                  value={ciTriggerJobID}
                  onChange={(e) => setCITriggerJobID(e.target.value)}
                  className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
                >
                  <option value="">Entire workflow</option>
                  {ciTriggerWorkflow.jobs.map((job) => (
                    <option key={job.id} value={job.id}>
                      {job.name} ({job.id})
                    </option>
                  ))}
                </select>
                <p className="text-xs text-muted-foreground">
                  Leave this as <span className="font-mono">Entire workflow</span> to let Kindling choose the full workflow plan.
                </p>
              </div>

              {Object.keys(ciTriggerInputs).length > 0 ? (
                <div className="space-y-3">
                  <p className="text-sm font-medium">Dispatch inputs</p>
                  {Object.entries(ciTriggerInputs).map(([name, value]) => (
                    <div key={name} className="space-y-2">
                      <Label htmlFor={`ci-input-${name}`} className="font-mono text-xs">
                        {name}
                      </Label>
                      <Input
                        id={`ci-input-${name}`}
                        value={value}
                        onChange={(e) =>
                          setCITriggerInputs((current) => ({
                            ...current,
                            [name]: e.target.value,
                          }))
                        }
                        className="font-mono"
                      />
                    </div>
                  ))}
                </div>
              ) : null}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">Pick a workflow before starting a run.</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setCITriggerDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={() => void handleRunCIWorkflow()} disabled={ciTriggerSubmitting || !ciTriggerWorkflow}>
              {ciTriggerSubmitting ? "Starting…" : "Run on main"}
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
