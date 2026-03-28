import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, useSearchParams } from "react-router-dom"
import {
  api,
  authProviderStartURL,
  type Server,
  type APIMeta,
  type AuthAdminProvider,
  type AuthIdentity,
  type AuthPublicProvider,
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

function displayServerName(server: Server): string {
  return server.hostname || server.id
}

export function SettingsPage() {
  const { session } = useAuth()
  const [searchParams] = useSearchParams()
  const [servers, setServers] = useState<Server[]>([])
  const [meta, setMeta] = useState<APIMeta | null>(null)
  const [publicUrlInput, setPublicUrlInput] = useState("")
  const [dashboardHostInput, setDashboardHostInput] = useState("")
  const [previewBaseDomainInput, setPreviewBaseDomainInput] = useState("")
  const [previewRetentionInput, setPreviewRetentionInput] = useState("3600")
  const [previewIdleInput, setPreviewIdleInput] = useState("300")
  const [scaleToZeroIdleInput, setScaleToZeroIdleInput] = useState("300")
  const [coldStartTimeoutInput, setColdStartTimeoutInput] = useState("120")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [providers, setProviders] = useState<ProviderRow[]>([])
  const [publicAuthProviders, setPublicAuthProviders] = useState<AuthPublicProvider[]>([])
  const [authProviders, setAuthProviders] = useState<AuthAdminProvider[]>([])
  const [identities, setIdentities] = useState<AuthIdentity[]>([])
  const [authSavingProvider, setAuthSavingProvider] = useState<"github" | "oidc" | null>(null)
  const [provBusy, setProvBusy] = useState(false)
  const [serverActionId, setServerActionId] = useState<string | null>(null)
  const [authForms, setAuthForms] = useState(() => formsFromProviders([]))
  const [newProv, setNewProv] = useState({
    provider: "github" as "github" | "gitlab",
    external_slug: "",
    display_label: "",
    token: "",
  })

  const canManageOrg = session?.authenticated && (session.role === "owner" || session.role === "admin")
  const isPlatformAdmin = session?.authenticated ? session.platform_admin : false
  const canManageServers = isPlatformAdmin
  const canManageProviders = canManageOrg
  const canManageAuth = isPlatformAdmin

  const load = useCallback(async () => {
    const [s, m, p, authEnabled, linkedIdentities, adminProviders] = await Promise.all([
      canManageServers ? api.listServers() : Promise.resolve([] as Server[]),
      isPlatformAdmin ? api.getMeta() : Promise.resolve(null as APIMeta | null),
      api.listOrgProviderConnections(),
      api.authProviders(),
      api.listAuthIdentities(),
      canManageAuth ? api.listAdminAuthProviders() : Promise.resolve([] as AuthAdminProvider[]),
    ])
    setServers(s)
    setMeta(m)
    setPublicUrlInput(m?.public_base_url || "")
    setDashboardHostInput(m?.dashboard_public_host || "")
    setPreviewBaseDomainInput(m?.preview_base_domain || "")
    setPreviewRetentionInput(String(m?.preview_retention_after_close_seconds ?? 3600))
    setPreviewIdleInput(String(m?.preview_idle_scale_seconds ?? 300))
    setScaleToZeroIdleInput(String(m?.scale_to_zero_idle_seconds ?? 300))
    setColdStartTimeoutInput(String(m?.cold_start_timeout_seconds ?? 120))
    setProviders(p as ProviderRow[])
    setPublicAuthProviders(authEnabled)
    setIdentities(linkedIdentities)
    setAuthProviders(adminProviders)
    if (canManageAuth) {
      setAuthForms(formsFromProviders(adminProviders))
    }
  }, [canManageAuth, canManageServers, isPlatformAdmin])

  useEffect(() => {
    void load()
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [load])

  useEffect(() => {
    if (!canManageServers) return
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
  }, [canManageServers, load])

  useEffect(() => {
    if (!canManageServers) return
    const timer = setInterval(() => {
      void load().catch((e) => setError(e instanceof APIError ? e.message : String(e)))
    }, 15000)
    return () => clearInterval(timer)
  }, [canManageServers, load])

  const linkedProviderSet = useMemo(() => new Set(identities.map((identity) => identity.provider)), [identities])
  const authTabDefault =
    searchParams.get("tab") ||
    (searchParams.get("auth_error") || searchParams.get("auth_linked") ? "authentication" : isPlatformAdmin ? "public-url" : "authentication")
  const authStatusMessage = searchParams.get("auth_linked")
  const authRedirectError = searchParams.get("auth_error")

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
        scale_to_zero_idle_seconds: Number.parseInt(scaleToZeroIdleInput, 10) || 300,
        cold_start_timeout_seconds: Number.parseInt(coldStartTimeoutInput, 10) || 120,
      })
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
      setDashboardHostInput(m.dashboard_public_host || "")
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

        <Tabs defaultValue={authTabDefault} orientation="vertical" className="min-w-0 md:items-start gap-4 md:gap-6">
          <TabsList variant="line" className="w-full overflow-x-auto md:w-48 shrink-0 md:sticky md:top-6">
            {isPlatformAdmin ? (
              <TabsTrigger value="public-url">
                <GlobeIcon className="size-4" /> Public URL
              </TabsTrigger>
            ) : null}
            <TabsTrigger value="authentication">
              <PlugIcon className="size-4" /> Authentication
            </TabsTrigger>
            {canManageServers ? (
              <TabsTrigger value="cluster">
                <ServerIcon className="size-4" /> Cluster
              </TabsTrigger>
            ) : null}
            <TabsTrigger value="providers">
              <PlugIcon className="size-4" /> Providers
            </TabsTrigger>
          </TabsList>

          {/* ── Public URL ──────────────────────────────────── */}
          {isPlatformAdmin ? (
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
                      <p className="text-[11px] text-muted-foreground">How long a production project may sit idle before the edge marks it eligible for scale-to-zero.</p>
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
                      <p className="text-[11px] text-muted-foreground">How long the edge waits for a scaled-to-zero deployment to come back before returning service unavailable.</p>
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
          ) : null}

          {/* ── Authentication ──────────────────────────────── */}
          <TabsContent value="authentication" className="min-w-0 space-y-6">
            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Linked Sign-In Methods</SurfaceTitle>
                <SurfaceDescription>Connect external identities to your Kindling account for browser sign-in.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                {authStatusMessage ? (
                  <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-emerald-700 dark:text-emerald-300">
                    Linked {authStatusMessage} successfully.
                  </div>
                ) : null}
                {authRedirectError ? (
                  <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-destructive">
                    {authRedirectError}
                  </div>
                ) : null}
                {identities.length === 0 ? (
                  <p className="text-muted-foreground">No external sign-in methods linked yet.</p>
                ) : (
                  <ul className="space-y-2">
                    {identities.map((identity) => (
                      <li key={identity.provider} className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3.5">
                        <div className="min-w-0">
                          <p className="font-medium text-sm">{identity.provider_display_name || identity.provider_login || identity.provider}</p>
                          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                            <span>{identity.provider}</span>
                            {identity.provider_login ? <span className="font-mono">{identity.provider_login}</span> : null}
                            {identity.provider_email ? <span>{identity.provider_email}</span> : null}
                            {identity.last_login_at ? <span>last used {new Date(identity.last_login_at).toLocaleString()}</span> : null}
                          </div>
                        </div>
                        <Badge variant="outline">Linked</Badge>
                      </li>
                    ))}
                  </ul>
                )}
                {publicAuthProviders.length > 0 ? (
                  <div className="space-y-3 rounded-lg border p-4">
                    <p className="font-medium text-sm">Link another sign-in method</p>
                    <div className="flex flex-wrap gap-2">
                      {publicAuthProviders.map((provider) => (
                        <Button
                          key={provider.provider}
                          size="sm"
                          variant="outline"
                          disabled={linkedProviderSet.has(provider.provider)}
                          onClick={() => {
                            window.location.assign(authProviderStartURL(provider.provider, "link", "/settings?tab=authentication"))
                          }}
                        >
                          {linkedProviderSet.has(provider.provider) ? `${provider.display_name} linked` : `Link ${provider.display_name}`}
                        </Button>
                      ))}
                    </div>
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground">No external sign-in providers are enabled for this cluster yet.</p>
                )}
              </SurfaceBody>
            </Surface>

            <Surface>
              <SurfaceHeader>
                <SurfaceTitle>Provider Configuration</SurfaceTitle>
                <SurfaceDescription>Cluster-level OAuth and OIDC settings that power dashboard sign-in.</SurfaceDescription>
              </SurfaceHeader>
              <SurfaceBody className="space-y-5 text-sm">
                {canManageAuth ? (
                  (["github", "oidc"] as const).map((providerKey) => {
                    const provider = authProviders.find((item) => item.provider === providerKey)
                    const form = authForms[providerKey]
                    return (
                      <div key={providerKey} className="rounded-xl border p-4 space-y-4">
                        <div className="flex flex-wrap items-center justify-between gap-3">
                          <div>
                            <p className="font-medium text-sm">{provider?.display_name || authProviderDefaults[providerKey].display_name}</p>
                            <p className="text-xs text-muted-foreground">
                              {providerKey === "github"
                                ? "GitHub OAuth app for browser sign-in and account linking."
                                : "Generic OpenID Connect provider for linked-account sign-in."}
                            </p>
                          </div>
                          <div className="flex flex-wrap gap-2">
                            <Badge variant={provider?.enabled ? "default" : "outline"}>{provider?.enabled ? "Enabled" : "Disabled"}</Badge>
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
                            placeholder={provider?.has_client_secret ? "Leave blank to keep the current secret" : "Enter client secret"}
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
                            Callback URL: <code className="code-block inline py-0.5 px-1.5 text-[0.7rem]">{provider.callback_url}</code>
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
                          <Button size="sm" disabled={authSavingProvider === providerKey} onClick={() => void saveAuthProvider(providerKey)}>
                            {authSavingProvider === providerKey ? "Saving…" : "Save provider"}
                          </Button>
                          {provider?.has_client_secret ? <span className="text-xs text-muted-foreground self-center">A client secret is already stored.</span> : null}
                        </div>
                      </div>
                    )
                  })
                ) : (
                  <p className="text-xs text-muted-foreground">Only platform admins can manage external sign-in providers.</p>
                )}
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
          {canManageServers ? (
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
          ) : null}
        </Tabs>
      </PageSection>
    </PageContainer>
  )
}
