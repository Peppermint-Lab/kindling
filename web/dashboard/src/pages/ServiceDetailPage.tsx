import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, useParams } from "react-router-dom"
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

export function ServiceDetailPage() {
  const { id } = useParams<{ id: string }>()
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

  const [newDomainName, setNewDomainName] = useState("")
  const [domainSaving, setDomainSaving] = useState(false)
  const [verifyingDomainId, setVerifyingDomainId] = useState<string | null>(null)

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
      const svc = await api.getService(id)
      setService(svc)
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
  }, [id, loadDeployments, loadDomains, loadSecrets, loadVolume])

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
          Service-scoped deploys, private endpoints, domains, secrets, and persistent storage for <span className="font-mono">{service.slug}</span>.
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
                <SurfaceTitle>Service config</SurfaceTitle>
                <SurfaceDescription>Service-level build and networking settings.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody>
                <MetadataGrid>
                  <MetadataItem label="Project">
                    <Link to={`/projects/${service.project_id}`} className="underline underline-offset-4">
                      {service.project_id}
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
                  {service.org_network_cidr ? (
                    <MetadataItem label="Org network">
                      <span className="font-mono text-sm">{service.org_network_cidr}</span>
                    </MetadataItem>
                  ) : null}
                </MetadataGrid>
              </SurfaceBody>
              <SurfaceSeparator />
              <SurfaceBody className="space-y-3">
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
              </SurfaceBody>
            </Surface>
          </PageSection>

          <PageSection>
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Private endpoints</SurfaceTitle>
                <SurfaceDescription>Internal DNS and port mappings for this service.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-3">
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
                <SurfaceDescription>Recent service deployments and the current reachability state.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-4">
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
                      <Link
                        key={deployment.id}
                        to={`/deployments/${deployment.id}`}
                        className="flex items-center justify-between gap-3 rounded-xl border p-3 hover:bg-muted/40 transition-colors"
                      >
                        <div className="min-w-0">
                          <p className="font-mono text-sm truncate">{deployment.github_commit}</p>
                          <p className="text-xs text-muted-foreground truncate">{deployment.id}</p>
                        </div>
                        <Badge variant={phaseVariant(deployment.phase)}>{phaseLabel(deployment.phase)}</Badge>
                      </Link>
                    ))}
                  </div>
                )}
              </SurfaceBody>
            </Surface>
          </PageSection>
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
