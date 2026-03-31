import { useCallback, useEffect, useMemo, useState } from "react"
import { Link, useSearchParams } from "react-router-dom"
import {
  api,
  authProviderStartURL,
  type Server,
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
import { ServerIcon, PlugIcon } from "lucide-react"
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
import { resolveOrgSettingsDefaultTab } from "@/lib/settings-tab-defaults"

function apiErrorMessage(e: unknown): string {
  return e instanceof APIError ? e.message : String(e)
}

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
  const [searchParams] = useSearchParams()
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [providers, setProviders] = useState<ProviderRow[]>([])
  const [publicAuthProviders, setPublicAuthProviders] = useState<AuthPublicProvider[]>([])
  const [identities, setIdentities] = useState<AuthIdentity[]>([])
  const [provBusy, setProvBusy] = useState(false)
  const [serverActionId, setServerActionId] = useState<string | null>(null)
  const [newProv, setNewProv] = useState({
    provider: "github" as "github" | "gitlab",
    external_slug: "",
    display_label: "",
    token: "",
  })

  const canManageOrg = Boolean(
    session?.authenticated && (session.role === "owner" || session.role === "admin"),
  )
  const isPlatformAdmin = session != null && session.authenticated === true && session.platform_admin

  const load = useCallback(async () => {
    const [s, p, authEnabled, linkedIdentities] = await Promise.all([
      canManageOrg ? api.listServers() : Promise.resolve([] as Server[]),
      api.listOrgProviderConnections(),
      api.authProviders(),
      api.listAuthIdentities(),
    ])
    setServers(s)
    setProviders(p as ProviderRow[])
    setPublicAuthProviders(authEnabled)
    setIdentities(linkedIdentities)
  }, [canManageOrg])

  useEffect(() => {
    void load()
      .catch((e) => setError(apiErrorMessage(e)))
      .finally(() => setLoading(false))
  }, [load])

  useEffect(() => {
    if (!canManageOrg) return
    let debounceTimer: ReturnType<typeof setTimeout> | null = null
    const scheduleReload = () => {
      if (debounceTimer != null) clearTimeout(debounceTimer)
      debounceTimer = setTimeout(() => {
        debounceTimer = null
        void load().catch((e) => setError(apiErrorMessage(e)))
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
  }, [canManageOrg, load])

  useEffect(() => {
    if (!canManageOrg) return
    const timer = setInterval(() => {
      void load().catch((e) => setError(apiErrorMessage(e)))
    }, 15000)
    return () => clearInterval(timer)
  }, [canManageOrg, load])

  const linkedProviderSet = useMemo(() => new Set(identities.map((identity) => identity.provider)), [identities])

  const authTabDefault = resolveOrgSettingsDefaultTab({
    tab: searchParams.get("tab"),
    authError: searchParams.get("auth_error"),
    authLinked: searchParams.get("auth_linked"),
    canManageOrg,
  })
  const authStatusMessage = searchParams.get("auth_linked")
  const authRedirectError = searchParams.get("auth_error")

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

        {isPlatformAdmin ? (
          <p className="text-sm text-muted-foreground mb-3">
            <Link to="/platform-settings" className="text-primary underline-offset-4 hover:underline font-medium">
              Control plane settings
            </Link>
            {" — "}public URL, preview domains, and cluster sign-in provider configuration.
          </p>
        ) : null}

        {error && <PageErrorBanner message={error} />}

        <Tabs defaultValue={authTabDefault} orientation="vertical" className="min-w-0 md:items-start gap-4 md:gap-6">
          <TabsList variant="line" className="w-full overflow-x-auto md:w-48 shrink-0 md:sticky md:top-6">
            <TabsTrigger value="authentication">
              <PlugIcon className="size-4" /> Authentication
            </TabsTrigger>
            {canManageOrg ? (
              <TabsTrigger value="cluster">
                <ServerIcon className="size-4" /> Workers
              </TabsTrigger>
            ) : null}
            <TabsTrigger value="providers">
              <PlugIcon className="size-4" /> Providers
            </TabsTrigger>
          </TabsList>

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
                      {publicAuthProviders.map((provider) => {
                        const linked = linkedProviderSet.has(provider.provider)
                        return (
                          <Button
                            key={provider.provider}
                            size="sm"
                            variant="outline"
                            disabled={linked}
                            onClick={() => {
                              window.location.assign(authProviderStartURL(provider.provider, "link", "/settings?tab=authentication"))
                            }}
                          >
                            {linked ? `${provider.display_name} linked` : `Link ${provider.display_name}`}
                          </Button>
                        )
                      })}
                    </div>
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground">No external sign-in providers are enabled for this cluster yet.</p>
                )}
                {isPlatformAdmin ? (
                  <p className="text-xs text-muted-foreground">
                    To configure GitHub / OIDC apps for the whole cluster, open{" "}
                    <Link to="/platform-settings?tab=sign-in-providers" className="text-primary underline-offset-4 hover:underline">
                      Control plane → Sign-in providers
                    </Link>
                    .
                  </p>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    Cluster sign-in configuration is managed by platform administrators.
                  </p>
                )}
              </SurfaceBody>
            </Surface>
          </TabsContent>

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
                        {canManageOrg && (
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              void (async () => {
                                try {
                                  await api.deleteOrgProviderConnection(p.id)
                                  await load()
                                } catch (e) {
                                  setError(apiErrorMessage(e))
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
                {canManageOrg ? (
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
                            setError(apiErrorMessage(e))
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

          {canManageOrg ? (
            <TabsContent value="cluster" className="min-w-0">
              <Surface>
                <SurfaceHeader>
                  <SurfaceTitle>Workers</SurfaceTitle>
                  <SurfaceDescription>
                    Cluster workers: health and workload placement for this organization. Drain and activate are control
                    plane actions.
                  </SurfaceDescription>
                </SurfaceHeader>
                <SurfaceBody>
                  {servers.length === 0 ? (
                    <p className="text-sm text-muted-foreground py-4 text-center">No servers registered yet.</p>
                  ) : (
                    <div className="space-y-3">
                      {servers.map((server) => (
                        <div key={server.id} className="rounded-xl border p-4 space-y-4">
                          <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                            <div className="min-w-0 space-y-2">
                              <div className="flex flex-wrap items-center gap-2">
                                <p className="font-mono text-sm font-medium truncate">{displayServerName(server)}</p>
                                <Badge variant="outline" className={healthChipClass(server.health)}>
                                  {server.health || "unknown"}
                                </Badge>
                                <Badge variant={server.status === "active" ? "default" : "secondary"}>
                                  {server.status}
                                </Badge>
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
                              {isPlatformAdmin && server.status === "active" && (
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
                                        setError(apiErrorMessage(e))
                                      } finally {
                                        setServerActionId(null)
                                      }
                                    })()
                                  }}
                                >
                                  Drain
                                </Button>
                              )}
                              {isPlatformAdmin && (server.status === "draining" || server.status === "drained") && (
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
                                        setError(apiErrorMessage(e))
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
