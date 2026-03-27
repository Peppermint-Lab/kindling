import { useCallback, useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { api, type Server, type APIMeta, APIError, dashboardEventTopics, subscribeDashboardEvents } from "@/lib/api"
import { useAuth } from "@/contexts/AuthContext"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ServerIcon, GlobeIcon, PlugIcon } from "lucide-react"
import {
  PageContainer,
  PageTitle,
  PageSection,
  PageErrorBanner,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceDescription,
  SurfaceBody,
} from "@/components/page-surface"
import { componentLabel, formatAgeSeconds, healthChipClass } from "@/lib/server-observability"

type ProviderRow = {
  id: string
  provider: string
  external_slug: string
  display_label: string
  has_credentials: boolean
  metadata: unknown
  created_at: string
  updated_at: string
}

function displayServerName(server: Server): string {
  return server.hostname || server.id
}

export function SettingsPage() {
  const { session } = useAuth()
  const [servers, setServers] = useState<Server[]>([])
  const [meta, setMeta] = useState<APIMeta | null>(null)
  const [publicUrlInput, setPublicUrlInput] = useState("")
  const [dashboardHostInput, setDashboardHostInput] = useState("")
  const [previewBaseDomainInput, setPreviewBaseDomainInput] = useState("")
  const [previewRetentionInput, setPreviewRetentionInput] = useState("3600")
  const [previewIdleInput, setPreviewIdleInput] = useState("300")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [providers, setProviders] = useState<ProviderRow[]>([])
  const [provBusy, setProvBusy] = useState(false)
  const [serverActionId, setServerActionId] = useState<string | null>(null)
  const [newProv, setNewProv] = useState({
    provider: "github" as "github" | "gitlab",
    external_slug: "",
    display_label: "",
    token: "",
  })

  const canManageServers =
    session?.authenticated && (session.role === "owner" || session.role === "admin")

  const load = useCallback(
    () =>
      Promise.all([api.listServers(), api.getMeta(), api.listOrgProviderConnections()]).then(([s, m, p]) => {
        setServers(s)
        setMeta(m)
        setPublicUrlInput(m.public_base_url || "")
        setDashboardHostInput(m.dashboard_public_host || "")
        setPreviewBaseDomainInput(m.preview_base_domain || "")
        setPreviewRetentionInput(String(m.preview_retention_after_close_seconds ?? 3600))
        setPreviewIdleInput(String(m.preview_idle_scale_seconds ?? 300))
        setProviders(p as ProviderRow[])
      }),
    [],
  )

  useEffect(() => {
    void load()
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [load])

  useEffect(() => {
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void load().catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.servers],
      onInvalidate: scheduleReload,
    })
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
    }
  }, [load])

  useEffect(() => {
    const timer = setInterval(() => {
      void load().catch((e) => setError(e instanceof APIError ? e.message : String(e)))
    }, 15000)
    return () => clearInterval(timer)
  }, [load])

  const canManageProviders =
    session?.authenticated && (session.role === "owner" || session.role === "admin")

  const handleSavePublicURL = async () => {
    setSaving(true)
    setError(null)
    try {
      const m = await api.updateMeta({
        public_base_url: publicUrlInput.trim(),
        dashboard_public_host: dashboardHostInput.trim(),
        preview_base_domain: previewBaseDomainInput.trim(),
        preview_retention_after_close_seconds: Number.parseInt(previewRetentionInput, 10) || 0,
        preview_idle_scale_seconds: Number.parseInt(previewIdleInput, 10) || 300,
      })
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
      setDashboardHostInput(m.dashboard_public_host || "")
      setPreviewBaseDomainInput(m.preview_base_domain || "")
      setPreviewRetentionInput(String(m.preview_retention_after_close_seconds ?? 3600))
      setPreviewIdleInput(String(m.preview_idle_scale_seconds ?? 300))
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <PageContainer>
        <div className="space-y-6">
          <Skeleton className="h-8 w-32" />
          <Skeleton className="h-48 rounded-xl" />
        </div>
      </PageContainer>
    )
  }

  return (
    <PageContainer>
      <PageSection>
        <PageTitle>Settings</PageTitle>

        {error && <PageErrorBanner message={error} />}

        <Tabs defaultValue="public-url" orientation="vertical" className="min-w-0 md:items-start gap-4 md:gap-6">
          <TabsList variant="line" className="w-full overflow-x-auto md:w-48 shrink-0 md:sticky md:top-6">
            <TabsTrigger value="public-url">
              <GlobeIcon className="size-4" /> Public URL
            </TabsTrigger>
            <TabsTrigger value="cluster">
              <ServerIcon className="size-4" /> Cluster
            </TabsTrigger>
            <TabsTrigger value="providers">
              <PlugIcon className="size-4" /> Providers
            </TabsTrigger>
          </TabsList>

          {/* ── Public URL ──────────────────────────────────── */}
          <TabsContent value="public-url" className="min-w-0">
            <Surface>
              <SurfaceBody className="pt-5 sm:pt-6 space-y-5 text-sm">
                <p className="text-muted-foreground leading-relaxed">
                  <span className="font-mono">public_base_url</span> is the API origin (webhooks,{" "}
                  <span className="font-mono">/api/*</span>), e.g. <span className="font-mono">https://api.example.com</span>.
                  Optional <span className="font-mono">dashboard_public_host</span> is the hostname for the SPA only (e.g.{" "}
                  <span className="font-mono">app.example.com</span>) when you split app and API on the edge.
                </p>
                <div className="space-y-2 max-w-xl">
                  <Label htmlFor="public-url">Public API base URL</Label>
                  <Input
                    id="public-url"
                    placeholder="https://api.kindling.example.com"
                    className="font-mono text-sm"
                    value={publicUrlInput}
                    onChange={(e) => setPublicUrlInput(e.target.value)}
                  />
                </div>
                <div className="space-y-2 max-w-xl">
                  <Label htmlFor="dashboard-host">Dashboard hostname (optional)</Label>
                  <Input
                    id="dashboard-host"
                    placeholder="app.kindling.example.com"
                    className="font-mono text-sm"
                    value={dashboardHostInput}
                    onChange={(e) => setDashboardHostInput(e.target.value)}
                  />
                </div>
                <div className="rounded-xl border border-dashed border-border/80 p-4 space-y-3 max-w-2xl bg-muted/20">
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">PR preview URLs</p>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    Hostname suffix for generated previews (e.g.{" "}
                    <span className="font-mono">preview.example.com</span>). Point wildcard DNS{" "}
                    <span className="font-mono">*.preview.example.com</span> at the Kindling edge. Enable the{" "}
                    <span className="font-mono">pull_request</span> event on the repo webhook in GitHub. Closed
                    previews stop immediately, stay visible until retention expires, and are then deleted
                    automatically.
                  </p>
                  <div className="space-y-1 max-w-xl">
                    <Label htmlFor="preview-base">Preview base domain</Label>
                    <Input
                      id="preview-base"
                      placeholder="preview.example.com"
                      className="font-mono text-sm"
                      value={previewBaseDomainInput}
                      onChange={(e) => setPreviewBaseDomainInput(e.target.value)}
                    />
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2 max-w-xl">
                    <div className="space-y-1">
                      <Label htmlFor="preview-retention">Retention after PR close (seconds)</Label>
                      <Input
                        id="preview-retention"
                        type="number"
                        min={0}
                        className="font-mono text-sm"
                        value={previewRetentionInput}
                        onChange={(e) => setPreviewRetentionInput(e.target.value)}
                      />
                      <p className="text-[11px] text-muted-foreground">How long closed previews stay listed before full cleanup. Use <span className="font-mono">0</span> to delete immediately on close.</p>
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor="preview-idle">Preview idle scale-down (seconds)</Label>
                      <Input
                        id="preview-idle"
                        type="number"
                        min={1}
                        className="font-mono text-sm"
                        value={previewIdleInput}
                        onChange={(e) => setPreviewIdleInput(e.target.value)}
                      />
                      <p className="text-[11px] text-muted-foreground">How long an active preview can sit idle before Kindling scales it to zero.</p>
                    </div>
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <Button size="sm" onClick={() => void handleSavePublicURL()} disabled={saving}>
                    {saving ? "Saving…" : "Save"}
                  </Button>
                  {meta?.public_base_url_configured && (
                    <span className="text-xs text-muted-foreground">
                      Current: <code className="code-block inline py-0.5 px-1.5 text-[0.7rem]">{meta.public_base_url}</code>
                    </span>
                  )}
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  First-boot seeds: <span className="font-mono">KINDLING_PUBLIC_URL</span> /{" "}
                  <span className="font-mono">--public-url</span>, and <span className="font-mono">KINDLING_DASHBOARD_HOST</span>{" "}
                  / <span className="font-mono">--dashboard-host</span>, only when the corresponding row is missing.
                  Production build: set <span className="font-mono">VITE_API_URL</span> to the API base URL.
                </p>
              </SurfaceBody>
            </Surface>
          </TabsContent>

          {/* ── Providers ───────────────────────────────────── */}
          <TabsContent value="providers" className="min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Provider Connections</SurfaceTitle>
                <SurfaceDescription>GitHub / GitLab connections for this organization (metadata and encrypted tokens).</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                {providers.length === 0 ? (
                  <p className="text-muted-foreground">No provider connections yet.</p>
                ) : (
                  <ul className="space-y-2">
                    {providers.map((p) => (
                      <li
                        key={p.id}
                        className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3.5"
                      >
                        <div className="min-w-0">
                          <p className="font-medium text-sm">{p.display_label || p.external_slug}</p>
                          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1 text-xs text-muted-foreground">
                            <span>{p.provider}</span>
                            {p.external_slug && <span className="font-mono">{p.external_slug}</span>}
                            <span>{p.has_credentials ? "credentials stored" : "no token"}</span>
                          </div>
                        </div>
                        {canManageProviders && (
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              void (async () => {
                                try {
                                  await api.deleteOrgProviderConnection(p.id)
                                  await load()
                                } catch (e) {
                                  setError(e instanceof APIError ? e.message : String(e))
                                }
                              })()
                            }}
                          >
                            Remove
                          </Button>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
                {canManageProviders ? (
                  <div className="space-y-4 max-w-xl border rounded-lg p-4">
                    <p className="font-medium text-sm">Add connection</p>
                    <div className="space-y-2">
                      <Label>Provider</Label>
                      <select
                        className="w-full text-sm rounded-md border bg-background px-2.5 py-2"
                        value={newProv.provider}
                        onChange={(e) =>
                          setNewProv((x) => ({
                            ...x,
                            provider: e.target.value as "github" | "gitlab",
                          }))
                        }
                      >
                        <option value="github">GitHub</option>
                        <option value="gitlab">GitLab</option>
                      </select>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="p-slug">External slug (org / group)</Label>
                      <Input
                        id="p-slug"
                        className="font-mono text-sm"
                        placeholder="my-org"
                        value={newProv.external_slug}
                        onChange={(e) => setNewProv((x) => ({ ...x, external_slug: e.target.value }))}
                      />
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="p-label">Label (optional)</Label>
                      <Input
                        id="p-label"
                        value={newProv.display_label}
                        onChange={(e) => setNewProv((x) => ({ ...x, display_label: e.target.value }))}
                      />
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="p-token">Token (optional, encrypted at rest)</Label>
                      <Input
                        id="p-token"
                        type="password"
                        autoComplete="off"
                        value={newProv.token}
                        onChange={(e) => setNewProv((x) => ({ ...x, token: e.target.value }))}
                      />
                    </div>
                    <Button
                      size="sm"
                      disabled={provBusy || !newProv.external_slug.trim()}
                      onClick={() => {
                        void (async () => {
                          setProvBusy(true)
                          setError(null)
                          try {
                            await api.createOrgProviderConnection({
                              provider: newProv.provider,
                              external_slug: newProv.external_slug.trim(),
                              display_label: newProv.display_label.trim() || undefined,
                              token: newProv.token.trim() || undefined,
                            })
                            setNewProv({ provider: "github", external_slug: "", display_label: "", token: "" })
                            await load()
                          } catch (e) {
                            setError(e instanceof APIError ? e.message : String(e))
                          } finally {
                            setProvBusy(false)
                          }
                        })()
                      }}
                    >
                      {provBusy ? "Saving…" : "Add connection"}
                    </Button>
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground">Only owners and admins can manage provider connections.</p>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>

          {/* ── Cluster ─────────────────────────────────────── */}
          <TabsContent value="cluster" className="min-w-0">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Servers</SurfaceTitle>
                <SurfaceDescription>Quick glance overview for cluster health, instance load, and component freshness.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody>
                {servers.length === 0 ? (
                  <p className="text-sm text-muted-foreground py-4 text-center">No servers registered yet.</p>
                ) : (
                  <div className="space-y-3">
                    {servers.map((server) => (
                      <div
                        key={server.id}
                        className="rounded-xl border p-4 space-y-4"
                      >
                        <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                          <div className="min-w-0 space-y-2">
                            <div className="flex flex-wrap items-center gap-2">
                              <p className="font-mono text-sm font-medium truncate">{displayServerName(server)}</p>
                              <Badge variant="outline" className={healthChipClass(server.health)}>
                                {server.health || "unknown"}
                              </Badge>
                              <Badge variant={server.status === "active" ? "default" : "secondary"}>{server.status}</Badge>
                            </div>
                            <p className="text-xs text-muted-foreground truncate">
                              {server.internal_ip || "No IP"}
                              {server.runtime ? ` · ${server.runtime}` : ""}
                            </p>
                            <div className="flex flex-wrap gap-2">
                              <Badge variant="outline" className="border-border bg-muted/30 text-foreground">
                                Heartbeat {formatAgeSeconds(server.heartbeat_age_seconds)}
                              </Badge>
                              <Badge variant="outline" className="border-border bg-muted/30 text-foreground">
                                {server.active_instance_count ?? 0} active
                              </Badge>
                              <Badge variant="outline" className="border-border bg-muted/30 text-foreground">
                                {server.instance_count ?? 0} total
                              </Badge>
                            </div>
                          </div>
                          <div className="flex flex-wrap items-center gap-2">
                            <Button
                              size="sm"
                              variant="outline"
                              render={<Link to={`/settings/servers/${server.id}`} />}
                            >
                              View details
                            </Button>
                            {canManageServers && server.status === "active" && (
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={serverActionId === server.id}
                                onClick={() => {
                                  void (async () => {
                                    setServerActionId(server.id)
                                    setError(null)
                                    try {
                                      await api.drainServer(server.id)
                                      await load()
                                    } catch (e) {
                                      setError(e instanceof APIError ? e.message : String(e))
                                    } finally {
                                      setServerActionId(null)
                                    }
                                  })()
                                }}
                              >
                                Drain
                              </Button>
                            )}
                            {canManageServers && (server.status === "draining" || server.status === "drained") && (
                              <Button
                                size="sm"
                                variant="secondary"
                                disabled={serverActionId === server.id}
                                onClick={() => {
                                  void (async () => {
                                    setServerActionId(server.id)
                                    setError(null)
                                    try {
                                      await api.activateServer(server.id)
                                      await load()
                                    } catch (e) {
                                      setError(e instanceof APIError ? e.message : String(e))
                                    } finally {
                                      setServerActionId(null)
                                    }
                                  })()
                                }}
                              >
                                Activate
                              </Button>
                            )}
                          </div>
                        </div>
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
                      </div>
                    ))}
                  </div>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>
        </Tabs>
      </PageSection>
    </PageContainer>
  )
}
