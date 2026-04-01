import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, Navigate, useSearchParams } from "react-router-dom"
import {
  api,
  type APIMeta,
  type AuthAdminProvider,
  type Server,
  APIError,
  dashboardEventTopics,
  subscribeDashboardEvents,
} from "@/lib/api"
import { useAuth } from "@/contexts/AuthContext"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { GlobeIcon, HeartPulseIcon, PlugIcon } from "lucide-react"
import { componentLabel, formatAgeSeconds, healthChipClass } from "@/lib/server-observability"
import {
  PageContainer,
  PageTitle,
  PageSection,
  PageErrorBanner,
} from "@/components/page-layout"
import {
  Surface,
  SurfaceBody,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceDescription,
} from "@/components/page-surface"
import { resolvePlatformSettingsDefaultTab } from "@/lib/settings-tab-defaults"

function apiErrorMessage(e: unknown): string {
  return e instanceof APIError ? e.message : String(e)
}

function displayServerName(server: Server): string {
  return server.hostname || server.id
}

type AuthProviderForm = {
  display_name: string
  enabled: boolean
  client_id: string
  client_secret: string
  clear_client_secret: boolean
  issuer_url: string
  scopes: string
}

const authProviderDefaults: Record<"github" | "oidc", AuthProviderForm> = {
  github: {
    display_name: "GitHub",
    enabled: false,
    client_id: "",
    client_secret: "",
    clear_client_secret: false,
    issuer_url: "",
    scopes: "read:user user:email read:org",
  },
  oidc: {
    display_name: "OpenID Connect",
    enabled: false,
    client_id: "",
    client_secret: "",
    clear_client_secret: false,
    issuer_url: "",
    scopes: "openid profile email",
  },
}

function formsFromProviders(providers: AuthAdminProvider[]) {
  const next = {
    github: { ...authProviderDefaults.github },
    oidc: { ...authProviderDefaults.oidc },
  }
  for (const provider of providers) {
    next[provider.provider] = {
      display_name: provider.display_name,
      enabled: provider.enabled,
      client_id: provider.client_id || "",
      client_secret: "",
      clear_client_secret: false,
      issuer_url: provider.issuer_url || "",
      scopes: provider.scopes || authProviderDefaults[provider.provider].scopes,
    }
  }
  return next
}

export function PlatformSettingsPage() {
  const { session } = useAuth()
  const [searchParams] = useSearchParams()
  const [meta, setMeta] = useState<APIMeta | null>(null)
  const [publicUrlInput, setPublicUrlInput] = useState("")
  const [dashboardHostInput, setDashboardHostInput] = useState("")
  const [serviceBaseDomainInput, setServiceBaseDomainInput] = useState("")
  const [previewBaseDomainInput, setPreviewBaseDomainInput] = useState("")
  const [previewRetentionInput, setPreviewRetentionInput] = useState("3600")
  const [previewIdleInput, setPreviewIdleInput] = useState("300")
  const [scaleToZeroIdleInput, setScaleToZeroIdleInput] = useState("300")
  const [coldStartTimeoutInput, setColdStartTimeoutInput] = useState("120")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [authProviders, setAuthProviders] = useState<AuthAdminProvider[]>([])
  const [authSavingProvider, setAuthSavingProvider] = useState<"github" | "oidc" | null>(null)
  const [authForms, setAuthForms] = useState(() => formsFromProviders([]))
  const [platformServers, setPlatformServers] = useState<Server[]>([])
  const [healthError, setHealthError] = useState<string | null>(null)
  const [serverActionId, setServerActionId] = useState<string | null>(null)

  const isPlatformAdmin = session != null && session.authenticated === true && session.platform_admin

  const refreshPlatformHealth = useCallback(async () => {
    try {
      const servers = await api.listPlatformServers()
      setPlatformServers(servers)
      setHealthError(null)
    } catch (e) {
      setHealthError(apiErrorMessage(e))
    }
  }, [])

  const runPlatformServerAction = useCallback(
    async (serverId: string, action: () => Promise<unknown>) => {
      setServerActionId(serverId)
      setError(null)
      try {
        await action()
        await refreshPlatformHealth()
      } catch (e) {
        setError(apiErrorMessage(e))
      } finally {
        setServerActionId(null)
      }
    },
    [refreshPlatformHealth],
  )

  const load = useCallback(async () => {
    const [m, adminProviders] = await Promise.all([api.getMeta(), api.listAdminAuthProviders()])
    setMeta(m)
    setPublicUrlInput(m.public_base_url || "")
    setDashboardHostInput(m.dashboard_public_host || "")
    setServiceBaseDomainInput(m.service_base_domain || "")
    setPreviewBaseDomainInput(m.preview_base_domain || "")
    setPreviewRetentionInput(String(m.preview_retention_after_close_seconds ?? 3600))
    setPreviewIdleInput(String(m.preview_idle_scale_seconds ?? 300))
    setScaleToZeroIdleInput(String(m.scale_to_zero_idle_seconds ?? 300))
    setColdStartTimeoutInput(String(m.cold_start_timeout_seconds ?? 120))
    setAuthProviders(adminProviders)
    setAuthForms(formsFromProviders(adminProviders))
    await refreshPlatformHealth()
  }, [refreshPlatformHealth])

  useEffect(() => {
    if (!isPlatformAdmin) {
      setLoading(false)
      return
    }
    void load()
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [load, isPlatformAdmin])

  useEffect(() => {
    if (!isPlatformAdmin) {
      return
    }
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void refreshPlatformHealth()
      }, 400)
    }
    const unsub = subscribeDashboardEvents({
      topics: [dashboardEventTopics.servers],
      onInvalidate: scheduleReload,
    })
    const timer = setInterval(() => {
      void refreshPlatformHealth()
    }, 15000)
    return () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      unsub()
      clearInterval(timer)
    }
  }, [isPlatformAdmin, refreshPlatformHealth])

  const healthStats = useMemo(() => {
    let healthy = 0
    let degraded = 0
    let stale = 0
    let unknown = 0
    let draining = 0
    for (const s of platformServers) {
      const h = s.health ?? "unknown"
      if (h === "healthy") healthy++
      else if (h === "degraded") degraded++
      else if (h === "stale") stale++
      else unknown++
      if (s.status === "draining" || s.status === "drained") draining++
    }
    return {
      total: platformServers.length,
      healthy,
      degraded,
      stale,
      unknown,
      draining,
    }
  }, [platformServers])

  const tabDefault = resolvePlatformSettingsDefaultTab(searchParams.get("tab"))

  const handleSavePublicURL = async () => {
    setSaving(true)
    setError(null)
    try {
      const m = await api.updateMeta({
        public_base_url: publicUrlInput.trim(),
        dashboard_public_host: dashboardHostInput.trim(),
        service_base_domain: serviceBaseDomainInput.trim(),
        preview_base_domain: previewBaseDomainInput.trim(),
        preview_retention_after_close_seconds: Number.parseInt(previewRetentionInput, 10) || 0,
        preview_idle_scale_seconds: Number.parseInt(previewIdleInput, 10) || 300,
        scale_to_zero_idle_seconds: Number.parseInt(scaleToZeroIdleInput, 10) || 300,
        cold_start_timeout_seconds: Number.parseInt(coldStartTimeoutInput, 10) || 120,
      })
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
      setDashboardHostInput(m.dashboard_public_host || "")
      setServiceBaseDomainInput(m.service_base_domain || "")
      setPreviewBaseDomainInput(m.preview_base_domain || "")
      setPreviewRetentionInput(String(m.preview_retention_after_close_seconds ?? 3600))
      setPreviewIdleInput(String(m.preview_idle_scale_seconds ?? 300))
      setScaleToZeroIdleInput(String(m.scale_to_zero_idle_seconds ?? 300))
      setColdStartTimeoutInput(String(m.cold_start_timeout_seconds ?? 120))
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const saveAuthProvider = async (provider: "github" | "oidc") => {
    const form = authForms[provider]
    setAuthSavingProvider(provider)
    setError(null)
    try {
      await api.updateAdminAuthProvider(provider, {
        display_name: form.display_name.trim(),
        enabled: form.enabled,
        client_id: form.client_id.trim(),
        client_secret: form.client_secret.trim() || undefined,
        clear_client_secret: form.clear_client_secret,
        issuer_url: provider === "oidc" ? form.issuer_url.trim() : undefined,
        scopes: form.scopes.trim(),
      })
      await load()
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      setAuthSavingProvider(null)
    }
  }

  if (session == null || session.authenticated !== true) {
    return <Navigate to="/login" replace />
  }

  if (!isPlatformAdmin) {
    return <Navigate to="/settings" replace />
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
        <PageTitle>Control plane</PageTitle>
        <p className="text-sm text-muted-foreground mb-4">
          Cluster-wide URLs, sign-in providers, and system health. Only platform administrators can open this page.
        </p>

        {error && <PageErrorBanner message={error} />}

        <Tabs defaultValue={tabDefault} orientation="vertical" className="min-w-0 md:items-start gap-4 md:gap-6">
          <TabsList variant="line" className="w-full overflow-x-auto md:w-48 shrink-0 md:sticky md:top-6">
            <TabsTrigger value="public-url">
              <GlobeIcon className="size-4" /> Public URL
            </TabsTrigger>
            <TabsTrigger value="sign-in-providers">
              <PlugIcon className="size-4" /> Sign-in providers
            </TabsTrigger>
            <TabsTrigger value="health">
              <HeartPulseIcon className="size-4" /> Health
            </TabsTrigger>
          </TabsList>

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
                  <p className="text-xs font-medium uppercase tracking-wider text-muted-foreground">Service URLs</p>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    Optional hostname suffix for generated public production service URLs, for example{" "}
                    <span className="font-mono">apps.example.com</span>. Public HTTP services then get
                    platform-managed hostnames like <span className="font-mono">api-my-project.apps.example.com</span>.
                  </p>
                  <div className="space-y-1 max-w-xl">
                    <Label htmlFor="service-base">Service base domain</Label>
                    <Input
                      id="service-base"
                      placeholder="apps.example.com"
                      className="font-mono text-sm"
                      value={serviceBaseDomainInput}
                      onChange={(e) => setServiceBaseDomainInput(e.target.value)}
                    />
                  </div>
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
                      <p className="text-[11px] text-muted-foreground">
                        How long closed previews stay listed before full cleanup. Use{" "}
                        <span className="font-mono">0</span> to delete immediately on close.
                      </p>
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
                      <p className="text-[11px] text-muted-foreground">
                        How long an active preview can sit idle before Kindling scales it to zero.
                      </p>
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor="prod-idle">Production idle scale-down (seconds)</Label>
                      <Input
                        id="prod-idle"
                        type="number"
                        min={1}
                        className="font-mono text-sm"
                        value={scaleToZeroIdleInput}
                        onChange={(e) => setScaleToZeroIdleInput(e.target.value)}
                      />
                      <p className="text-[11px] text-muted-foreground">
                        How long a production project may sit idle before the edge marks it eligible for scale-to-zero.
                      </p>
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor="cold-start-timeout">Cold start timeout (seconds)</Label>
                      <Input
                        id="cold-start-timeout"
                        type="number"
                        min={1}
                        className="font-mono text-sm"
                        value={coldStartTimeoutInput}
                        onChange={(e) => setColdStartTimeoutInput(e.target.value)}
                      />
                      <p className="text-[11px] text-muted-foreground">
                        How long the edge waits for a scaled-to-zero deployment to come back before returning service
                        unavailable.
                      </p>
                    </div>
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <Button size="sm" onClick={() => void handleSavePublicURL()} disabled={saving}>
                    {saving ? "Saving…" : "Save"}
                  </Button>
                  {meta?.public_base_url_configured && (
                    <span className="text-xs text-muted-foreground">
                      Current:{" "}
                      <code className="code-block inline py-0.5 px-1.5 text-[0.7rem]">{meta.public_base_url}</code>
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

          <TabsContent value="sign-in-providers" className="min-w-0 space-y-6">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Provider configuration</SurfaceTitle>
                <SurfaceDescription>Cluster-level OAuth and OIDC settings that power dashboard sign-in.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                {(["github", "oidc"] as const).map((providerKey) => {
                  const provider = authProviders.find((item) => item.provider === providerKey)
                  const form = authForms[providerKey]
                  return (
                    <div key={providerKey} className="rounded-xl border p-4 space-y-4">
                      <div className="flex flex-wrap items-center justify-between gap-3">
                        <div>
                          <p className="font-medium text-sm">
                            {provider?.display_name || authProviderDefaults[providerKey].display_name}
                          </p>
                          <p className="text-xs text-muted-foreground">
                            {providerKey === "github"
                              ? "GitHub OAuth app for browser sign-in and account linking."
                              : "Generic OpenID Connect provider for linked-account sign-in."}
                          </p>
                        </div>
                        <div className="flex flex-wrap gap-2">
                          <Badge variant={provider?.enabled ? "default" : "outline"}>
                            {provider?.enabled ? "Enabled" : "Disabled"}
                          </Badge>
                          <Badge variant="outline">{provider?.configured ? "Configured" : "Needs setup"}</Badge>
                        </div>
                      </div>
                      <div className="grid gap-4 md:grid-cols-2">
                        <div className="space-y-2">
                          <Label htmlFor={`auth-${providerKey}-name`}>Display name</Label>
                          <Input
                            id={`auth-${providerKey}-name`}
                            value={form.display_name}
                            onChange={(e) =>
                              setAuthForms((current) => ({
                                ...current,
                                [providerKey]: { ...current[providerKey], display_name: e.target.value },
                              }))
                            }
                          />
                        </div>
                        <div className="space-y-2">
                          <Label htmlFor={`auth-${providerKey}-client-id`}>Client ID</Label>
                          <Input
                            id={`auth-${providerKey}-client-id`}
                            className="font-mono text-sm"
                            value={form.client_id}
                            onChange={(e) =>
                              setAuthForms((current) => ({
                                ...current,
                                [providerKey]: { ...current[providerKey], client_id: e.target.value },
                              }))
                            }
                          />
                        </div>
                      </div>
                      {providerKey === "oidc" ? (
                        <div className="space-y-2 max-w-2xl">
                          <Label htmlFor="auth-oidc-issuer">Issuer URL</Label>
                          <Input
                            id="auth-oidc-issuer"
                            className="font-mono text-sm"
                            placeholder="https://accounts.example.com"
                            value={form.issuer_url}
                            onChange={(e) =>
                              setAuthForms((current) => ({
                                ...current,
                                oidc: { ...current.oidc, issuer_url: e.target.value },
                              }))
                            }
                          />
                        </div>
                      ) : null}
                      <div className="space-y-2 max-w-2xl">
                        <Label htmlFor={`auth-${providerKey}-secret`}>Client secret</Label>
                        <Input
                          id={`auth-${providerKey}-secret`}
                          type="password"
                          autoComplete="off"
                          placeholder={
                            provider?.has_client_secret ? "Leave blank to keep the current secret" : "Enter client secret"
                          }
                          value={form.client_secret}
                          onChange={(e) =>
                            setAuthForms((current) => ({
                              ...current,
                              [providerKey]: { ...current[providerKey], client_secret: e.target.value },
                            }))
                          }
                        />
                        <label className="flex items-center gap-2 text-xs text-muted-foreground">
                          <input
                            type="checkbox"
                            checked={form.clear_client_secret}
                            onChange={(e) =>
                              setAuthForms((current) => ({
                                ...current,
                                [providerKey]: { ...current[providerKey], clear_client_secret: e.target.checked },
                              }))
                            }
                          />
                          Clear stored secret on save
                        </label>
                      </div>
                      <div className="space-y-2 max-w-2xl">
                        <Label htmlFor={`auth-${providerKey}-scopes`}>Scopes</Label>
                        <Input
                          id={`auth-${providerKey}-scopes`}
                          className="font-mono text-sm"
                          value={form.scopes}
                          onChange={(e) =>
                            setAuthForms((current) => ({
                              ...current,
                              [providerKey]: { ...current[providerKey], scopes: e.target.value },
                            }))
                          }
                        />
                      </div>
                      {provider?.callback_url ? (
                        <p className="text-xs text-muted-foreground">
                          Callback URL:{" "}
                          <code className="code-block inline py-0.5 px-1.5 text-[0.7rem]">{provider.callback_url}</code>
                        </p>
                      ) : null}
                      <label className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={form.enabled}
                          onChange={(e) =>
                            setAuthForms((current) => ({
                              ...current,
                              [providerKey]: { ...current[providerKey], enabled: e.target.checked },
                            }))
                          }
                        />
                        Enable {providerKey === "github" ? "GitHub" : "OIDC"} sign-in
                      </label>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          size="sm"
                          disabled={authSavingProvider === providerKey}
                          onClick={() => void saveAuthProvider(providerKey)}
                        >
                          {authSavingProvider === providerKey ? "Saving…" : "Save provider"}
                        </Button>
                        {provider?.has_client_secret ? (
                          <span className="text-xs text-muted-foreground self-center">
                            A client secret is already stored.
                          </span>
                        ) : null}
                      </div>
                    </div>
                  )
                })}
              </SurfaceBody>
            </Surface>
          </TabsContent>

          <TabsContent value="health" className="min-w-0 space-y-6">
            {healthError ? <PageErrorBanner message={healthError} /> : null}
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>System health</SurfaceTitle>
                <SurfaceDescription>
                  All registered workers in the cluster (not limited to the current organization). Auto-refreshes every
                  15 seconds and on server events.
                </SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-6 text-sm">
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Total</p>
                    <p className="text-2xl font-semibold tabular-nums">{healthStats.total}</p>
                  </div>
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Healthy</p>
                    <p className="text-2xl font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">
                      {healthStats.healthy}
                    </p>
                  </div>
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Degraded</p>
                    <p className="text-2xl font-semibold tabular-nums text-amber-600 dark:text-amber-400">
                      {healthStats.degraded}
                    </p>
                  </div>
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Stale</p>
                    <p className="text-2xl font-semibold tabular-nums text-rose-600 dark:text-rose-400">{healthStats.stale}</p>
                  </div>
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Unknown</p>
                    <p className="text-2xl font-semibold tabular-nums text-muted-foreground">{healthStats.unknown}</p>
                  </div>
                  <div className="rounded-xl border border-border/80 bg-muted/20 px-3 py-3">
                    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Draining / drained</p>
                    <p className="text-2xl font-semibold tabular-nums">{healthStats.draining}</p>
                  </div>
                </div>

                {platformServers.length === 0 && !healthError ? (
                  <p className="text-sm text-muted-foreground py-2 text-center">No servers registered yet.</p>
                ) : null}

                {platformServers.length > 0 ? (
                  <div className="space-y-3">
                    {platformServers.map((server) => (
                      <div key={server.id} className="rounded-xl border p-4 space-y-4">
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
                                {server.running_instance_count ?? 0} running
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
                              render={<Link to={`/settings/servers/${server.id}?from=platform`} />}
                            >
                              View details
                            </Button>
                            {server.status === "active" ? (
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={serverActionId === server.id}
                                onClick={() =>
                                  void runPlatformServerAction(server.id, () => api.drainServer(server.id))
                                }
                              >
                                Drain
                              </Button>
                            ) : null}
                            {server.status === "draining" || server.status === "drained" ? (
                              <Button
                                size="sm"
                                variant="secondary"
                                disabled={serverActionId === server.id}
                                onClick={() =>
                                  void runPlatformServerAction(server.id, () => api.activateServer(server.id))
                                }
                              >
                                Activate
                              </Button>
                            ) : null}
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
                ) : null}
              </SurfaceBody>
            </Surface>
          </TabsContent>
        </Tabs>
      </PageSection>
    </PageContainer>
  )
}
