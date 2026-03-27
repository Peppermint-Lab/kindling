import { useCallback, useEffect, useState } from "react"
import { useParams } from "react-router-dom"
import { api, type Server, type ServerComponent, type ServerDetail, APIError } from "@/lib/api"
import { useAuth } from "@/contexts/AuthContext"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  PageBackLink,
  PageContainer,
  PageDescription,
  PageErrorBanner,
  PageHeader,
  PageSection,
  PageTitle,
  MetadataGrid,
  MetadataItem,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceBody,
  SurfaceDescription,
  SurfaceHeader,
  SurfaceTitle,
} from "@/components/page-surface"
import { componentLabel, formatAgeSeconds, formatBytes, healthChipClass } from "@/lib/server-observability"

function displayServerName(server: Server): string {
  return server.hostname || server.id
}

function resourceHealthLabel(health: string): string {
  switch (health) {
    case "fresh":
      return "Fresh"
    case "stale":
      return "Stale"
    default:
      return "Missing"
  }
}

function renderLastSeen(component: ServerComponent): string {
  if (component.last_success_at) {
    return new Date(component.last_success_at).toLocaleString()
  }
  if (component.observed_at) {
    return new Date(component.observed_at).toLocaleString()
  }
  return "—"
}

export function ServerDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { session } = useAuth()
  const [detail, setDetail] = useState<ServerDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [serverActionBusy, setServerActionBusy] = useState(false)

  const canManageServers =
    session?.authenticated && (session.role === "owner" || session.role === "admin")

  const load = useCallback(async () => {
    if (!id) return
    const next = await api.getServerDetails(id)
    setDetail(next)
  }, [id])

  useEffect(() => {
    let cancelled = false
    if (!id) return
    setLoading(true)
    setError(null)
    void load()
      .catch((e) => {
        if (!cancelled) {
          setError(e instanceof APIError ? e.message : String(e))
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [id, load])

  useEffect(() => {
    if (!id) return
    const timer = setInterval(() => {
      void load().catch(() => {
        /* keep last successful data visible */
      })
    }, 15000)
    return () => clearInterval(timer)
  }, [id, load])

  if (loading && !detail) {
    return (
      <PageContainer size="wide">
        <div className="space-y-4">
          <Skeleton className="h-5 w-32" />
          <Skeleton className="h-8 w-56" />
          <Skeleton className="h-36 rounded-xl" />
          <Skeleton className="h-40 rounded-xl" />
          <Skeleton className="h-64 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  if (!detail) {
    return (
      <PageContainer>
        <PageErrorBanner message={error || "Server not found"} className="max-w-xl" />
      </PageContainer>
    )
  }

  const server = detail.summary

  async function handleServerAction(action: "drain" | "activate") {
    if (!id) return
    setServerActionBusy(true)
    setError(null)
    try {
      if (action === "drain") {
        await api.drainServer(id)
      } else {
        await api.activateServer(id)
      }
      await load()
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      setServerActionBusy(false)
    }
  }

  return (
    <PageContainer size="wide">
      <PageSection>
        <div>
          <PageBackLink to="/settings">Back to settings</PageBackLink>
          <PageHeader>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <PageTitle>{displayServerName(server)}</PageTitle>
                <Badge variant="outline" className={healthChipClass(server.health)}>
                  {server.health || "unknown"}
                </Badge>
                <Badge variant={server.status === "active" ? "default" : "secondary"}>{server.status}</Badge>
              </div>
              <PageDescription className="mt-2">
                Auto-refreshes every 15 seconds. Host: <span className="font-mono">{server.internal_ip || "—"}</span>
              </PageDescription>
            </div>
            {canManageServers ? (
              <div className="flex flex-wrap items-center gap-2">
                {server.status === "active" ? (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={serverActionBusy}
                    onClick={() => void handleServerAction("drain")}
                  >
                    Drain
                  </Button>
                ) : null}
                {(server.status === "draining" || server.status === "drained") ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={serverActionBusy}
                    onClick={() => void handleServerAction("activate")}
                  >
                    Activate
                  </Button>
                ) : null}
              </div>
            ) : null}
          </PageHeader>
        </div>

        {error ? <PageErrorBanner message={error} /> : null}

        <Surface>
          <SurfaceHeader>
            <SurfaceTitle>Overview</SurfaceTitle>
            <SurfaceDescription>Quick cluster and runtime diagnostics for this server.</SurfaceDescription>
          </SurfaceHeader>
          <SurfaceBody className="space-y-5 text-sm">
            <MetadataGrid>
              <MetadataItem label="Overall health">
                <Badge variant="outline" className={healthChipClass(server.health)}>
                  {server.health || "unknown"}
                </Badge>
              </MetadataItem>
              <MetadataItem label="Heartbeat">
                <div className="space-y-1">
                  <Badge variant="outline" className={healthChipClass(server.heartbeat_health)}>
                    {server.heartbeat_health || "unknown"}
                  </Badge>
                  <p className="text-xs text-muted-foreground">
                    {formatAgeSeconds(server.heartbeat_age_seconds)}
                    {server.last_heartbeat_at ? ` · ${new Date(server.last_heartbeat_at).toLocaleString()}` : ""}
                  </p>
                </div>
              </MetadataItem>
              <MetadataItem label="Instances">
                <span className="font-mono">
                  {server.running_instance_count ?? 0} running / {server.active_instance_count ?? 0} active /{" "}
                  {server.instance_count ?? 0} total
                </span>
              </MetadataItem>
              <MetadataItem label="Runtime">{server.runtime || "—"}</MetadataItem>
              <MetadataItem label="Enabled components" span="2">
                {server.enabled_components && server.enabled_components.length > 0
                  ? server.enabled_components.map(componentLabel).join(", ")
                  : "—"}
              </MetadataItem>
            </MetadataGrid>
            <div className="flex flex-wrap gap-2">
              {(server.components ?? []).map((component) => (
                <Badge
                  key={component.component}
                  variant="outline"
                  className={healthChipClass(component.health, component.enabled ? "" : "opacity-70")}
                >
                  {componentLabel(component.component)} · {component.enabled ? component.health : "off"}
                </Badge>
              ))}
            </div>
          </SurfaceBody>
        </Surface>

        <Surface>
          <SurfaceHeader>
            <SurfaceTitle>Control Plane</SurfaceTitle>
            <SurfaceDescription>Latest known component heartbeat, success time, and error state.</SurfaceDescription>
          </SurfaceHeader>
          <SurfaceBody className="grid gap-4 lg:grid-cols-2">
            {(server.components ?? []).map((component) => (
              <div key={component.component} className="rounded-xl border p-4 space-y-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <p className="font-medium">{componentLabel(component.component)}</p>
                    <p className="text-xs text-muted-foreground">
                      {component.enabled ? "Enabled" : "Not reporting on this host"}
                    </p>
                  </div>
                  <Badge variant="outline" className={healthChipClass(component.health)}>
                    {component.enabled ? component.health : "off"}
                  </Badge>
                </div>
                <MetadataGrid className="gap-3 sm:grid-cols-2">
                  <MetadataItem label="Last success">{renderLastSeen(component)}</MetadataItem>
                  <MetadataItem label="Observed at">
                    {component.observed_at ? new Date(component.observed_at).toLocaleString() : "—"}
                  </MetadataItem>
                  <MetadataItem label="Last error">
                    {component.last_error_at ? new Date(component.last_error_at).toLocaleString() : "—"}
                  </MetadataItem>
                  <MetadataItem label="Raw status">{component.raw_status || "—"}</MetadataItem>
                </MetadataGrid>
                {component.metadata && Object.keys(component.metadata).length > 0 ? (
                  <div className="flex flex-wrap gap-2">
                    {Object.entries(component.metadata).map(([key, value]) => (
                      <Badge key={key} variant="outline" className="border-border bg-muted/30 text-muted-foreground">
                        {key}: {String(value)}
                      </Badge>
                    ))}
                  </div>
                ) : null}
                {component.last_error_message ? (
                  <p className="rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
                    {component.last_error_message}
                  </p>
                ) : null}
              </div>
            ))}
          </SurfaceBody>
        </Surface>

        <Surface>
          <SurfaceHeader>
            <SurfaceTitle>Instances</SurfaceTitle>
            <SurfaceDescription>Latest per-instance resource snapshot on this server.</SurfaceDescription>
          </SurfaceHeader>
          <SurfaceBody className="overflow-x-auto">
            {detail.instances.length === 0 ? (
              <p className="text-sm text-muted-foreground">No deployment instances are currently attached to this server.</p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="py-2 pr-4 font-medium text-xs">Project</th>
                    <th className="py-2 pr-4 font-medium text-xs">Instance</th>
                    <th className="py-2 pr-4 font-medium text-xs">Lifecycle</th>
                    <th className="py-2 pr-4 font-medium text-xs">Resources</th>
                    <th className="py-2 pr-4 font-medium text-xs">Sample</th>
                    <th className="py-2 pr-4 font-medium text-xs">Runtime</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.instances.map((instance) => (
                    <tr key={instance.deployment_instance_id} className="border-b border-border/60 align-top">
                      <td className="py-3 pr-4">
                        <p className="font-medium">{instance.project_name}</p>
                        <p className="font-mono text-xs text-muted-foreground">
                          {instance.deployment_id.slice(0, 8)}…
                        </p>
                      </td>
                      <td className="py-3 pr-4">
                        <p className="font-mono text-xs">{instance.deployment_instance_id.slice(0, 8)}…</p>
                        <p className="text-xs text-muted-foreground">{instance.vm_id ? `VM ${instance.vm_id.slice(0, 8)}…` : "No VM attached"}</p>
                      </td>
                      <td className="py-3 pr-4">
                        <div className="flex flex-wrap gap-2">
                          <Badge variant="outline" className="border-border bg-muted/30 text-foreground">
                            {instance.role}
                          </Badge>
                          <Badge variant="outline" className={healthChipClass(instance.status === "failed" ? "stale" : "unknown")}>
                            {instance.status}
                          </Badge>
                        </div>
                      </td>
                      <td className="py-3 pr-4">
                        <p className="tabular-nums">{instance.cpu_percent != null ? `${instance.cpu_percent.toFixed(1)}% CPU` : "— CPU"}</p>
                        <p className="tabular-nums text-muted-foreground">{formatBytes(instance.memory_rss_bytes)} RSS</p>
                        <p className="tabular-nums text-xs text-muted-foreground">
                          {formatBytes(instance.disk_read_bytes)} / {formatBytes(instance.disk_write_bytes)}
                        </p>
                      </td>
                      <td className="py-3 pr-4">
                        <div className="space-y-1">
                          <Badge variant="outline" className={healthChipClass(instance.resource_health === "fresh" ? "healthy" : instance.resource_health === "stale" ? "stale" : "unknown")}>
                            {resourceHealthLabel(instance.resource_health)}
                          </Badge>
                          <p className="text-xs text-muted-foreground">
                            {instance.sampled_at ? new Date(instance.sampled_at).toLocaleString() : "No sample yet"}
                          </p>
                          {instance.sample_age_seconds != null ? (
                            <p className="text-xs text-muted-foreground">{formatAgeSeconds(instance.sample_age_seconds)}</p>
                          ) : null}
                        </div>
                      </td>
                      <td className="py-3 pr-4 text-xs text-muted-foreground">{instance.source || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </SurfaceBody>
        </Surface>
      </PageSection>
    </PageContainer>
  )
}
