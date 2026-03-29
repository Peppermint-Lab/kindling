import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import {
  api,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
  type Deployment,
  type ProjectDomain,
  type ProjectVolume,
  type ProjectVolumeBackup,
  type Server,
  type Service,
  type ServiceEndpoint,
  type ServiceSecret,
} from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Skeleton } from "@/components/ui/skeleton"
import { DeploymentReachability } from "@/components/deployment-reachability"
import { phaseLabel, phaseVariant } from "@/lib/deploy-badge"
import { selectLatestRunningDeployment } from "@/lib/deployment-reachability"
import { deploymentPromotionConfirmMessage } from "@/lib/deployment-promotion"
import { sortServiceSecrets, upsertServiceSecretInList } from "@/lib/project-secrets"
import {
  EmptyState,
  MetadataGrid,
  MetadataItem,
  PageBackLink,
  PageContainer,
  PageDescription,
  PageErrorBanner,
  PageHeader,
  PageSection,
  PageTitle,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceBody,
  SurfaceDescription,
  SurfaceHeader,
  SurfaceSeparator,
  SurfaceTitle,
} from "@/components/page-surface"
import { useAuth } from "@/contexts/AuthContext"

type EndpointDraft = {
  name: string
  protocol: "http" | "tcp"
  target_port: number
  visibility: "private" | "public"
}

const defaultEndpointDraft: EndpointDraft = {
  name: "",
  protocol: "http",
  target_port: 3000,
  visibility: "private",
}

function endpointHealthLabel(endpoint: ServiceEndpoint): string {
  if (endpoint.last_healthy_at) {
    return `Healthy at ${new Date(endpoint.last_healthy_at).toLocaleString()}`
  }
  if (endpoint.last_unhealthy_at) {
    return `Last unhealthy at ${new Date(endpoint.last_unhealthy_at).toLocaleString()}`
  }
  return "No health signal recorded yet"
}

export function ServiceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { session } = useAuth()
  const [service, setService] = useState<Service | null>(null)
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const [domains, setDomains] = useState<ProjectDomain[]>([])
  const [secrets, setSecrets] = useState<ServiceSecret[]>([])
  const [projectVolume, setProjectVolume] = useState<ProjectVolume | null>(null)
  const [volumeBackups, setVolumeBackups] = useState<ProjectVolumeBackup[]>([])
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [mainTab, setMainTab] = useState("overview")

  const [commitSha, setCommitSha] = useState("")
  const [deploying, setDeploying] = useState(false)
  const [deploymentActionId, setDeploymentActionId] = useState<string | null>(null)

  const [newDomainName, setNewDomainName] = useState("")
  const [domainSaving, setDomainSaving] = useState(false)
  const [verifyingDomainId, setVerifyingDomainId] = useState<string | null>(null)
  const [endpointDraft, setEndpointDraft] = useState<EndpointDraft>(defaultEndpointDraft)
  const [editingEndpointId, setEditingEndpointId] = useState<string | null>(null)
  const [endpointSaving, setEndpointSaving] = useState(false)
  const [deletingEndpointId, setDeletingEndpointId] = useState<string | null>(null)

  const [secretName, setSecretName] = useState("")
  const [secretValue, setSecretValue] = useState("")
  const [secretSaving, setSecretSaving] = useState(false)
  const [deletingSecretId, setDeletingSecretId] = useState<string | null>(null)

  const [volumeMountPath, setVolumeMountPath] = useState("/data")
  const [volumeSizeGB, setVolumeSizeGB] = useState(10)
  const [volumeBackupSchedule, setVolumeBackupSchedule] = useState<"off" | "manual" | "daily" | "weekly">("manual")
  const [volumeBackupRetentionCount, setVolumeBackupRetentionCount] = useState(7)
  const [volumePreDeleteBackupEnabled, setVolumePreDeleteBackupEnabled] = useState(false)
  const [volumeTargetServerID, setVolumeTargetServerID] = useState("")
  const [volumeSaving, setVolumeSaving] = useState(false)
  const [volumeDeleting, setVolumeDeleting] = useState(false)
  const [volumeActionKind, setVolumeActionKind] = useState<"backup" | "restore" | "move" | "repair" | null>(null)
  const [volumeActionBackupID, setVolumeActionBackupID] = useState<string | null>(null)

  const latestRunningDeployment = useMemo(
    () => selectLatestRunningDeployment(deployments),
    [deployments],
  )
  const canManageService =
    session?.authenticated &&
    (session.platform_admin || session.role === "owner" || session.role === "admin")
  const canViewOperatorDetails = canManageService
  const publicEndpoints = useMemo(
    () => (service?.endpoints || []).filter((endpoint) => endpoint.visibility === "public"),
    [service?.endpoints],
  )
  const generatedPublicURL = publicEndpoints.find((endpoint) => endpoint.public_hostname)?.public_hostname || ""

  const loadService = useCallback(async (serviceId: string) => {
    const svc = await api.getService(serviceId)
    setService(svc)
    return svc
  }, [])

  const loadDeployments = useCallback(async (serviceId: string) => {
    const list = await api.listServiceDeployments(serviceId)
    setDeployments(list)
  }, [])

  const loadDomains = useCallback(async (serviceId: string) => {
    const list = await api.listServiceDomains(serviceId)
    setDomains(list)
  }, [])

  const loadSecrets = useCallback(async (serviceId: string) => {
    const list = await api.listServiceSecrets(serviceId)
    setSecrets(sortServiceSecrets(list))
  }, [])

  const loadVolume = useCallback(async (serviceId: string) => {
    try {
      const [volume, backups] = await Promise.all([
        api.getServiceVolume(serviceId),
        api.listServiceVolumeBackups(serviceId),
      ])
      setProjectVolume(volume)
      setVolumeBackups(backups)
      setVolumeMountPath(volume.mount_path)
      setVolumeSizeGB(volume.size_gb)
      setVolumeBackupSchedule(volume.backup_policy.schedule)
      setVolumeBackupRetentionCount(volume.backup_policy.retention_count)
      setVolumePreDeleteBackupEnabled(volume.backup_policy.pre_delete_backup_enabled)
    } catch (e) {
      if (e instanceof APIError && e.status === 404) {
        setProjectVolume(null)
        setVolumeBackups([])
        return
      }
      throw e
    }
  }, [])

  const load = useCallback(async () => {
    if (!id) return
    setLoading(true)
    setError(null)
    try {
      await loadService(id)
      await Promise.all([
        loadDeployments(id),
        loadDomains(id),
        loadSecrets(id),
        loadVolume(id),
        api.listServers().then(setServers),
      ])
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not load service")
    } finally {
      setLoading(false)
    }
  }, [id, loadDeployments, loadDomains, loadSecrets, loadService, loadVolume])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!service?.project_id) return
    return subscribeDashboardEvents({
      topics: [dashboardEventTopics.project(service.project_id)],
      onInvalidate: () => {
        void load()
      },
    })
  }, [load, service?.project_id])

  const handleDeploy = useCallback(async () => {
    if (!id) return
    setDeploying(true)
    setError(null)
    try {
      const deployment = await api.triggerServiceDeploy(id, commitSha.trim() || "main")
      setCommitSha("")
      setDeployments((current) => [deployment, ...current])
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not trigger deploy")
    } finally {
      setDeploying(false)
    }
  }, [commitSha, id])

  const handlePromoteDeployment = useCallback(
    async (deployment: Deployment) => {
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
    },
    [navigate],
  )

  const handleCreateDomain = useCallback(async () => {
    if (!id) return
    setDomainSaving(true)
    setError(null)
    try {
      const dom = await api.createServiceDomain(id, newDomainName.trim())
      setDomains((current) => [...current, dom].sort((a, b) => a.domain_name.localeCompare(b.domain_name)))
      setNewDomainName("")
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not add domain")
    } finally {
      setDomainSaving(false)
    }
  }, [id, newDomainName])

  const resetEndpointEditor = useCallback(() => {
    setEndpointDraft(defaultEndpointDraft)
    setEditingEndpointId(null)
  }, [])

  const handleEditEndpoint = useCallback((endpoint: ServiceEndpoint) => {
    setEditingEndpointId(endpoint.id)
    setEndpointDraft({
      name: endpoint.name,
      protocol: endpoint.protocol,
      target_port: endpoint.target_port,
      visibility: endpoint.visibility,
    })
  }, [])

  const handleSaveEndpoint = useCallback(async () => {
    if (!id) return
    setEndpointSaving(true)
    setError(null)
    try {
      const payload = {
        name: endpointDraft.name.trim(),
        protocol: endpointDraft.protocol,
        target_port: Math.max(1, Math.min(65535, Math.floor(Number(endpointDraft.target_port)) || 3000)),
        visibility: endpointDraft.visibility,
      }
      if (editingEndpointId) {
        await api.updateServiceEndpoint(id, editingEndpointId, payload)
      } else {
        await api.createServiceEndpoint(id, payload)
      }
      await loadService(id)
      resetEndpointEditor()
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not save endpoint")
    } finally {
      setEndpointSaving(false)
    }
  }, [editingEndpointId, endpointDraft, id, loadService, resetEndpointEditor])

  const handleDeleteEndpoint = useCallback(async (endpointId: string) => {
    if (!id) return
    setDeletingEndpointId(endpointId)
    setError(null)
    try {
      await api.deleteServiceEndpoint(id, endpointId)
      await loadService(id)
      if (editingEndpointId === endpointId) {
        resetEndpointEditor()
      }
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete endpoint")
    } finally {
      setDeletingEndpointId(null)
    }
  }, [editingEndpointId, id, loadService, resetEndpointEditor])

  const handleVerifyDomain = useCallback(async (domainId: string) => {
    if (!id) return
    setVerifyingDomainId(domainId)
    setError(null)
    try {
      const dom = await api.verifyServiceDomain(id, domainId)
      setDomains((current) => current.map((item) => (item.id === domainId ? dom : item)))
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not verify domain")
    } finally {
      setVerifyingDomainId(null)
    }
  }, [id])

  const handleDeleteDomain = useCallback(async (domainId: string) => {
    if (!id) return
    setError(null)
    try {
      await api.deleteServiceDomain(id, domainId)
      setDomains((current) => current.filter((item) => item.id !== domainId))
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete domain")
    }
  }, [id])

  const handleSaveSecret = useCallback(async () => {
    if (!id) return
    setSecretSaving(true)
    setError(null)
    try {
      const secret = await api.upsertServiceSecret(id, {
        name: secretName.trim(),
        value: secretValue,
      })
      setSecrets((current) => upsertServiceSecretInList(current, secret))
      setSecretName("")
      setSecretValue("")
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not save service secret")
    } finally {
      setSecretSaving(false)
    }
  }, [id, secretName, secretValue])

  const handleDeleteSecret = useCallback(async (secretId: string) => {
    if (!id) return
    setDeletingSecretId(secretId)
    setError(null)
    try {
      await api.deleteServiceSecret(id, secretId)
      setSecrets((current) => current.filter((secret) => secret.id !== secretId))
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not delete service secret")
    } finally {
      setDeletingSecretId(null)
    }
  }, [id])

  const handleSaveVolume = useCallback(async () => {
    if (!id) return
    setVolumeSaving(true)
    setError(null)
    try {
      const volume = await api.putServiceVolume(id, {
        mount_path: volumeMountPath.trim() || "/data",
        size_gb: Math.max(1, Math.floor(Number(volumeSizeGB)) || 10),
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
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not save persistent volume")
    } finally {
      setVolumeSaving(false)
    }
  }, [id, volumeBackupRetentionCount, volumeBackupSchedule, volumeMountPath, volumePreDeleteBackupEnabled, volumeSizeGB])

  const refreshVolume = useCallback(async () => {
    if (!id) return
    try {
      await loadVolume(id)
    } catch (e) {
      setError(e instanceof APIError ? e.message : "Could not refresh service volume")
    }
  }, [id, loadVolume])

  const handleDeleteVolume = useCallback(async () => {
    if (!id) return
    setVolumeDeleting(true)
    setError(null)
    try {
      await api.deleteServiceVolume(id)
      await refreshVolume()
    } catch (e) {
      if (e instanceof APIError && e.status === 404) {
        setProjectVolume(null)
        setVolumeBackups([])
      } else {
        setError(e instanceof APIError ? e.message : "Could not delete persistent volume")
      }
    } finally {
      setVolumeDeleting(false)
    }
  }, [id, refreshVolume])

  const handleVolumeAction = useCallback(async (kind: "backup" | "move" | "repair" | "restore", backupId?: string) => {
    if (!id) return
    setVolumeActionKind(kind)
    setVolumeActionBackupID(backupId ?? null)
    setError(null)
    try {
      if (kind === "backup") {
        await api.createServiceVolumeBackup(id)
      } else if (kind === "move") {
        await api.moveServiceVolume(id, { target_server_id: volumeTargetServerID.trim() || undefined })
      } else if (kind === "repair") {
        await api.repairServiceVolume(id)
      } else if (kind === "restore" && backupId) {
        await api.restoreServiceVolume(id, { backup_id: backupId, target_server_id: volumeTargetServerID.trim() || undefined })
      }
      await refreshVolume()
    } catch (e) {
      setError(e instanceof APIError ? e.message : `Could not ${kind} service volume`)
    } finally {
      setVolumeActionKind(null)
      setVolumeActionBackupID(null)
    }
  }, [id, refreshVolume, volumeTargetServerID])

  if (loading) {
    return (
      <PageContainer>
        <PageHeader>
          <Skeleton className="h-6 w-48" />
          <Skeleton className="h-4 w-72" />
        </PageHeader>
      </PageContainer>
    )
  }

  if (!service) {
    return (
      <PageContainer>
        <PageErrorBanner message={error ?? "Service not found"} />
      </PageContainer>
    )
  }

  const volumeBusy =
    projectVolume?.current_operation != null ||
    volumeActionKind != null ||
    projectVolume?.status === "backing_up" ||
    projectVolume?.status === "restoring" ||
    projectVolume?.status === "repairing" ||
    projectVolume?.status === "deleting"

  return (
    <PageContainer>
      <PageHeader>
        <PageBackLink to={`/projects/${service.project_id}`}>Back to project</PageBackLink>
        <PageTitle>{service.name}</PageTitle>
        <PageDescription>
          Service-scoped deploys, endpoints, domains, secrets, and persistent storage for{" "}
          <span className="font-mono">{service.slug}</span> in{" "}
          <Link to={`/projects/${service.project_id}`} className="underline underline-offset-4">
            {service.project_name}
          </Link>.
        </PageDescription>
      </PageHeader>

      {error ? <PageErrorBanner message={error} /> : null}

      <Tabs value={mainTab} onValueChange={setMainTab} className="space-y-4">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="domains">Domains</TabsTrigger>
          <TabsTrigger value="secrets">Secrets</TabsTrigger>
          <TabsTrigger value="volume">Volume</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-4">
          <PageSection>
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Public endpoints</SurfaceTitle>
                <SurfaceDescription>
                  Services stay private by default. Public exposure is explicit and reversible.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-3">
                {publicEndpoints.length === 0 ? (
                  <EmptyState
                    title="Private by default"
                    description="This service has no public HTTP endpoint yet. Add or edit an endpoint below to expose a generated URL or attach custom domains."
                  />
                ) : (
                  <div className="space-y-3">
                    {publicEndpoints.map((endpoint) => (
                      <div key={endpoint.id} className="rounded-xl border p-4 space-y-3">
                        <div className="flex items-center justify-between gap-3">
                          <div>
                            <p className="font-medium">{endpoint.name}</p>
                            <p className="text-xs text-muted-foreground">
                              {endpoint.protocol.toUpperCase()}:{endpoint.target_port}
                            </p>
                          </div>
                          <Badge>Public</Badge>
                        </div>
                        {endpoint.public_hostname ? (
                          <div className="rounded-lg bg-muted/40 p-3 space-y-1">
                            <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                              Generated URL
                            </p>
                            <p className="font-mono text-sm break-all">{endpoint.public_hostname}</p>
                          </div>
                        ) : (
                          <p className="text-xs text-muted-foreground">
                            No generated URL yet. A public hostname appears after a production deployment is running and the platform service base domain is configured.
                          </p>
                        )}
                      </div>
                    ))}
                  </div>
                )}
                <SurfaceSeparator />
                <div className="space-y-3">
                  <div>
                    <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Custom domains</p>
                    <p className="text-xs text-muted-foreground mt-1">
                      Optional domains route through the Kindling edge to this service.
                    </p>
                  </div>
                  {domains.length === 0 ? (
                    <p className="text-sm text-muted-foreground">No custom domains attached.</p>
                  ) : (
                    <div className="space-y-2">
                      {domains.map((domain) => (
                        <div key={domain.id} className="rounded-lg border p-3">
                          <div className="flex items-center justify-between gap-3">
                            <p className="font-medium break-all">{domain.domain_name}</p>
                            <Badge variant={domain.verified_at ? "default" : "secondary"}>
                              {domain.verified_at ? "Verified" : "Pending verification"}
                            </Badge>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </SurfaceBody>
            </Surface>
          </PageSection>

          <PageSection>
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Private endpoints</SurfaceTitle>
                <SurfaceDescription>
                  Stable internal DNS names and ports for this service. Even public services still resolve privately first.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-3">
                <MetadataGrid>
                  <MetadataItem label="Project">
                    <Link to={`/projects/${service.project_id}`} className="underline underline-offset-4">
                      {service.project_name}
                    </Link>
                  </MetadataItem>
                  <MetadataItem label="Slug">
                    <span className="font-mono text-sm">{service.slug}</span>
                  </MetadataItem>
                  <MetadataItem label="Root directory">
                    <span className="font-mono text-sm">{service.root_directory}</span>
                  </MetadataItem>
                  <MetadataItem label="Dockerfile">
                    <span className="font-mono text-sm">{service.dockerfile_path}</span>
                  </MetadataItem>
                  <MetadataItem label="Desired replicas">
                    <span className="font-mono text-sm">{service.desired_instance_count}</span>
                  </MetadataItem>
                  <MetadataItem label="Default exposure">
                    <Badge variant={service.public_default ? "default" : "secondary"}>
                      {service.public_default ? "public" : "private"}
                    </Badge>
                  </MetadataItem>
                </MetadataGrid>
                {canManageService ? (
                  <>
                    <SurfaceSeparator />
                    <div className="space-y-3">
                      <div>
                        <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Endpoint management</p>
                        <p className="text-xs text-muted-foreground mt-1">
                          One public HTTP endpoint per service in this release. TCP endpoints remain private-only.
                        </p>
                      </div>
                      <div className="grid gap-3 md:grid-cols-2">
                        <div className="space-y-2">
                          <Label htmlFor="endpoint-name">Endpoint name</Label>
                          <Input
                            id="endpoint-name"
                            value={endpointDraft.name}
                            onChange={(event) =>
                              setEndpointDraft((current) => ({ ...current, name: event.target.value }))
                            }
                            placeholder="web"
                          />
                        </div>
                        <div className="space-y-2">
                          <Label htmlFor="endpoint-port">Target port</Label>
                          <Input
                            id="endpoint-port"
                            type="number"
                            min={1}
                            max={65535}
                            value={endpointDraft.target_port}
                            onChange={(event) =>
                              setEndpointDraft((current) => ({
                                ...current,
                                target_port: Number(event.target.value) || 3000,
                              }))
                            }
                          />
                        </div>
                      </div>
                      <div className="grid gap-3 md:grid-cols-2">
                        <div className="space-y-2">
                          <Label>Protocol</Label>
                          <div className="flex gap-2">
                            <Button
                              type="button"
                              variant={endpointDraft.protocol === "http" ? "default" : "outline"}
                              onClick={() =>
                                setEndpointDraft((current) => ({
                                  ...current,
                                  protocol: "http",
                                }))
                              }
                            >
                              HTTP
                            </Button>
                            <Button
                              type="button"
                              variant={endpointDraft.protocol === "tcp" ? "default" : "outline"}
                              onClick={() =>
                                setEndpointDraft((current) => ({
                                  ...current,
                                  protocol: "tcp",
                                  visibility: current.visibility === "public" ? "private" : current.visibility,
                                }))
                              }
                            >
                              TCP
                            </Button>
                          </div>
                        </div>
                        <div className="space-y-2">
                          <Label>Visibility</Label>
                          <div className="flex gap-2">
                            <Button
                              type="button"
                              variant={endpointDraft.visibility === "private" ? "default" : "outline"}
                              onClick={() =>
                                setEndpointDraft((current) => ({ ...current, visibility: "private" }))
                              }
                            >
                              Private
                            </Button>
                            <Button
                              type="button"
                              variant={endpointDraft.visibility === "public" ? "default" : "outline"}
                              disabled={endpointDraft.protocol !== "http"}
                              onClick={() =>
                                setEndpointDraft((current) => ({ ...current, visibility: "public" }))
                              }
                            >
                              Public HTTP
                            </Button>
                          </div>
                        </div>
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button onClick={() => void handleSaveEndpoint()} disabled={endpointSaving}>
                          {endpointSaving
                            ? editingEndpointId
                              ? "Saving…"
                              : "Creating…"
                            : editingEndpointId
                              ? "Save endpoint"
                              : "Add endpoint"}
                        </Button>
                        {editingEndpointId ? (
                          <Button type="button" variant="outline" onClick={resetEndpointEditor}>
                            Cancel edit
                          </Button>
                        ) : null}
                      </div>
                    </div>
                  </>
                ) : null}
                <SurfaceSeparator />
                {service.endpoints && service.endpoints.length > 0 ? (
                  service.endpoints.map((endpoint) => (
                    <div key={endpoint.id} className="rounded-xl border p-4 space-y-2">
                      <div className="flex items-center justify-between gap-3">
                        <div>
                          <p className="font-medium">{endpoint.name}</p>
                          <p className="text-xs text-muted-foreground">
                            {endpoint.protocol.toUpperCase()}:{endpoint.target_port}
                          </p>
                        </div>
                        <Badge variant={endpoint.visibility === "public" ? "default" : "secondary"}>
                          {endpoint.visibility}
                        </Badge>
                      </div>
                      <p className="font-mono text-xs break-all">{endpoint.dns_name}</p>
                      <p className="text-xs text-muted-foreground">
                        Private IP: <span className="font-mono text-foreground">{endpoint.private_ip}</span>
                      </p>
                      <p className="text-xs text-muted-foreground">{endpointHealthLabel(endpoint)}</p>
                      {canManageService ? (
                        <div className="flex flex-wrap gap-2 pt-1">
                          <Button type="button" variant="outline" size="sm" onClick={() => handleEditEndpoint(endpoint)}>
                            Edit
                          </Button>
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={() => void handleDeleteEndpoint(endpoint.id)}
                            disabled={deletingEndpointId === endpoint.id}
                          >
                            {deletingEndpointId === endpoint.id ? "Deleting…" : "Delete"}
                          </Button>
                        </div>
                      ) : null}
                    </div>
                  ))
                ) : (
                  <EmptyState title="No endpoints yet" description="This service has no declared private endpoints yet." />
                )}
              </SurfaceBody>
            </Surface>
          </PageSection>

          <PageSection>
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Deployments</SurfaceTitle>
                <SurfaceDescription>Recent service deployments and current reachability.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-4">
                <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
                  <div className="space-y-2">
                    <Label htmlFor="service-deploy-commit">Deploy commit or branch</Label>
                    <Input
                      id="service-deploy-commit"
                      value={commitSha}
                      onChange={(event) => setCommitSha(event.target.value)}
                      placeholder="main"
                    />
                  </div>
                  <div className="flex items-end">
                    <Button onClick={() => void handleDeploy()} disabled={deploying}>
                      {deploying ? "Deploying…" : "Deploy service"}
                    </Button>
                  </div>
                </div>
                <SurfaceSeparator />
                {latestRunningDeployment ? (
                  <div className="rounded-xl border p-4 space-y-3">
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <p className="font-medium">Latest running deployment</p>
                        <p className="font-mono text-xs text-muted-foreground break-all">{latestRunningDeployment.id}</p>
                      </div>
                      <Badge variant={phaseVariant(latestRunningDeployment.phase)}>{phaseLabel(latestRunningDeployment.phase)}</Badge>
                    </div>
                    <DeploymentReachability reachable={latestRunningDeployment.reachable} />
                  </div>
                ) : null}
                {deployments.length === 0 ? (
                  <EmptyState title="No deployments yet" description="Deploy this service to start creating runtime history." />
                ) : (
                  <div className="space-y-2">
                    {deployments.slice(0, 10).map((deployment) => (
                      <div key={deployment.id} className="flex items-center gap-2">
                        <Link
                          to={`/deployments/${deployment.id}`}
                          className="flex min-w-0 flex-1 items-center justify-between gap-3 rounded-xl border p-3 transition-colors hover:bg-muted/40"
                        >
                          <div className="min-w-0">
                            <p className="font-mono text-sm truncate">{deployment.github_commit}</p>
                            <p className="text-xs text-muted-foreground truncate">{deployment.id}</p>
                            {deployment.promoted_from_deployment_id ? (
                              <p className="font-mono text-xs text-muted-foreground truncate">
                                Rollback of {deployment.promoted_from_deployment_id.slice(0, 8)}
                              </p>
                            ) : null}
                          </div>
                          <Badge variant={phaseVariant(deployment.phase)}>{phaseLabel(deployment.phase)}</Badge>
                        </Link>
                        {deployment.can_promote_to_production ? (
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            disabled={deploymentActionId === deployment.id}
                            onClick={() => void handlePromoteDeployment(deployment)}
                          >
                            {deploymentActionId === deployment.id ? "Promoting…" : "Promote"}
                          </Button>
                        ) : null}
                      </div>
                    ))}
                  </div>
                )}
              </SurfaceBody>
            </Surface>
          </PageSection>

          {canViewOperatorDetails ? (
            <PageSection>
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Operator details</SurfaceTitle>
                  <SurfaceDescription>
                    Advanced runtime coordinates for org admins and platform operators.
                  </SurfaceDescription>
                </SurfaceHeader>
                <SurfaceBody>
                  <MetadataGrid>
                    {service.org_network_cidr ? (
                      <MetadataItem label="Org network">
                        <span className="font-mono text-sm">{service.org_network_cidr}</span>
                      </MetadataItem>
                    ) : null}
                    <MetadataItem label="Generated public URL">
                      <span className="font-mono text-sm break-all">{generatedPublicURL || "Not exposed"}</span>
                    </MetadataItem>
                    <MetadataItem label="Runtime URL">
                      <span className="font-mono text-xs break-all">
                        {latestRunningDeployment?.reachable?.runtime_url || "No running deployment"}
                      </span>
                    </MetadataItem>
                    <MetadataItem label="VM IP">
                      <span className="font-mono text-xs break-all">
                        {latestRunningDeployment?.reachable?.vm_ip || "No running deployment"}
                      </span>
                    </MetadataItem>
                  </MetadataGrid>
                </SurfaceBody>
              </Surface>
            </PageSection>
          ) : null}
        </TabsContent>

        <TabsContent value="domains" className="space-y-4">
          <Surface>
            <SurfaceHeader>
              <SurfaceTitle>Public domains</SurfaceTitle>
              <SurfaceDescription>Attach explicit public hostnames to this service.</SurfaceDescription>
            </SurfaceHeader>
            <SurfaceBody className="space-y-4">
              <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
                <Input value={newDomainName} onChange={(event) => setNewDomainName(event.target.value)} placeholder="api.example.com" />
                <Button onClick={() => void handleCreateDomain()} disabled={domainSaving}>
                  {domainSaving ? "Adding…" : "Add domain"}
                </Button>
              </div>
              {domains.length === 0 ? (
                <EmptyState title="No service domains yet" description="Domains added here route to this service rather than the whole project." />
              ) : (
                <div className="space-y-3">
                  {domains.map((domain) => (
                    <div key={domain.id} className="rounded-xl border p-4 space-y-3">
                      <div className="flex items-center justify-between gap-3">
                        <div>
                          <p className="font-medium">{domain.domain_name}</p>
                          <p className="text-xs text-muted-foreground">
                            {domain.verified_at ? "Verified" : "Awaiting verification"}
                          </p>
                        </div>
                        <div className="flex gap-2">
                          {!domain.verified_at ? (
                            <Button variant="outline" onClick={() => void handleVerifyDomain(domain.id)} disabled={verifyingDomainId === domain.id}>
                              {verifyingDomainId === domain.id ? "Verifying…" : "Verify"}
                            </Button>
                          ) : null}
                          <Button variant="outline" onClick={() => void handleDeleteDomain(domain.id)}>Delete</Button>
                        </div>
                      </div>
                      {domain.dns_challenge ? (
                        <div className="rounded-lg bg-muted/40 p-3 text-xs space-y-1">
                          <p>TXT record</p>
                          <p className="font-mono break-all">{domain.dns_challenge.name}</p>
                          <p className="font-mono break-all">{domain.dns_challenge.value}</p>
                        </div>
                      ) : null}
                    </div>
                  ))}
                </div>
              )}
            </SurfaceBody>
          </Surface>
        </TabsContent>

        <TabsContent value="secrets" className="space-y-4">
          <Surface>
            <SurfaceHeader>
              <SurfaceTitle>Service secrets</SurfaceTitle>
              <SurfaceDescription>Service-scoped secrets override shared project defaults with the same name.</SurfaceDescription>
            </SurfaceHeader>
            <SurfaceBody className="space-y-4">
              <div className="grid gap-3 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="service-secret-name">Name</Label>
                  <Input id="service-secret-name" value={secretName} onChange={(event) => setSecretName(event.target.value)} placeholder="API_KEY" />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="service-secret-value">Value</Label>
                  <Input id="service-secret-value" value={secretValue} onChange={(event) => setSecretValue(event.target.value)} placeholder="secret value" />
                </div>
              </div>
              <Button onClick={() => void handleSaveSecret()} disabled={secretSaving}>
                {secretSaving ? "Saving…" : "Save service secret"}
              </Button>

              {secrets.length === 0 ? (
                <EmptyState title="No secrets yet" description="Inherited project defaults and service overrides will show up here." />
              ) : (
                <div className="space-y-2">
                  {secrets.map((secret) => (
                    <div key={secret.id} className="flex items-center justify-between gap-3 rounded-xl border p-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <p className="font-mono text-sm">{secret.name}</p>
                          <Badge variant={secret.scope === "service" ? "default" : "secondary"}>
                            {secret.scope === "service" ? "service override" : "project default"}
                          </Badge>
                        </div>
                        <p className="text-xs text-muted-foreground">
                          {secret.scope === "service"
                            ? `Owned by ${secret.service_name || service.name}`
                            : "Inherited from the project"}
                        </p>
                      </div>
                      <Button
                        variant="outline"
                        disabled={secret.scope !== "service" || deletingSecretId === secret.id}
                        onClick={() => void handleDeleteSecret(secret.id)}
                      >
                        {deletingSecretId === secret.id ? "Deleting…" : "Delete"}
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </SurfaceBody>
          </Surface>
        </TabsContent>

        <TabsContent value="volume" className="space-y-4">
          <Surface>
            <SurfaceHeader>
              <SurfaceTitle>Persistent volume</SurfaceTitle>
              <SurfaceDescription>Durable storage owned by this service.</SurfaceDescription>
            </SurfaceHeader>
            <SurfaceBody className="space-y-4">
              <div className="grid gap-4 md:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="service-volume-mount">Mount path</Label>
                  <Input id="service-volume-mount" value={volumeMountPath} onChange={(event) => setVolumeMountPath(event.target.value)} />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="service-volume-size">Size (GB)</Label>
                  <Input id="service-volume-size" type="number" min={1} value={volumeSizeGB} onChange={(event) => setVolumeSizeGB(Number(event.target.value))} />
                </div>
              </div>
              <div className="grid gap-4 md:grid-cols-3">
                <div className="space-y-2">
                  <Label htmlFor="service-volume-schedule">Backup schedule</Label>
                  <select
                    id="service-volume-schedule"
                    className="flex h-10 w-full rounded-md border bg-background px-3 text-sm"
                    value={volumeBackupSchedule}
                    onChange={(event) => setVolumeBackupSchedule(event.target.value as "off" | "manual" | "daily" | "weekly")}
                  >
                    <option value="off">Off</option>
                    <option value="manual">Manual</option>
                    <option value="daily">Daily</option>
                    <option value="weekly">Weekly</option>
                  </select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="service-volume-retention">Retention count</Label>
                  <Input id="service-volume-retention" type="number" min={1} value={volumeBackupRetentionCount} onChange={(event) => setVolumeBackupRetentionCount(Number(event.target.value))} />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="service-volume-target">Target server</Label>
                  <Input
                    id="service-volume-target"
                    list="service-volume-target-servers"
                    value={volumeTargetServerID}
                    onChange={(event) => setVolumeTargetServerID(event.target.value)}
                    placeholder="Optional UUID"
                  />
                  <datalist id="service-volume-target-servers">
                    {servers.map((server) => (
                      <option key={server.id} value={server.id}>
                        {server.hostname}
                      </option>
                    ))}
                  </datalist>
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={volumePreDeleteBackupEnabled}
                  onChange={(event) => setVolumePreDeleteBackupEnabled(event.target.checked)}
                />
                Create a final backup before delete
              </label>
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => void handleSaveVolume()} disabled={volumeSaving || volumeBusy}>
                  {volumeSaving ? "Saving…" : projectVolume ? "Update volume" : "Enable volume"}
                </Button>
                {projectVolume ? (
                  <>
                    <Button variant="outline" onClick={() => void handleVolumeAction("backup")} disabled={volumeBusy || volumeActionKind === "backup"}>
                      {volumeActionKind === "backup" ? "Backing up…" : "Create backup"}
                    </Button>
                    <Button variant="outline" onClick={() => void handleVolumeAction("move")} disabled={volumeBusy || volumeActionKind === "move"}>
                      {volumeActionKind === "move" ? "Moving…" : "Move"}
                    </Button>
                    <Button variant="outline" onClick={() => void handleVolumeAction("repair")} disabled={volumeBusy || volumeActionKind === "repair"}>
                      {volumeActionKind === "repair" ? "Repairing…" : "Repair"}
                    </Button>
                    <Button variant="outline" onClick={() => void handleDeleteVolume()} disabled={volumeDeleting || volumeBusy}>
                      {volumeDeleting ? "Deleting…" : "Delete volume"}
                    </Button>
                  </>
                ) : null}
              </div>
              {projectVolume ? (
                <div className="rounded-xl border p-4 space-y-3">
                  <MetadataGrid>
                    <MetadataItem label="Status">{projectVolume.status}</MetadataItem>
                    <MetadataItem label="Health">{projectVolume.health}</MetadataItem>
                    <MetadataItem label="Attached VM">{projectVolume.attached_vm_id || "Detached"}</MetadataItem>
                    <MetadataItem label="Pinned server">{projectVolume.server_id || "Unassigned"}</MetadataItem>
                  </MetadataGrid>
                  {projectVolume.last_error ? (
                    <p className="text-sm text-destructive">{projectVolume.last_error}</p>
                  ) : null}
                </div>
              ) : null}
              {volumeBackups.length > 0 ? (
                <div className="space-y-2">
                  {volumeBackups.map((backup) => (
                    <div key={backup.id} className="flex items-center justify-between gap-3 rounded-xl border p-3">
                      <div>
                        <p className="font-medium">{backup.kind}</p>
                        <p className="text-xs text-muted-foreground">{backup.status}</p>
                      </div>
                      <Button
                        variant="outline"
                        disabled={backup.status !== "succeeded" || volumeBusy || volumeActionKind === "restore"}
                        onClick={() => void handleVolumeAction("restore", backup.id)}
                      >
                        {volumeActionKind === "restore" && volumeActionBackupID === backup.id ? "Restoring…" : "Restore"}
                      </Button>
                    </div>
                  ))}
                </div>
              ) : null}
            </SurfaceBody>
          </Surface>
        </TabsContent>
      </Tabs>
    </PageContainer>
  )
}
