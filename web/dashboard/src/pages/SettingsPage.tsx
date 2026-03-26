import { useEffect, useState } from "react"
import { api, type Server, type APIMeta, APIError } from "@/lib/api"
import { useAuth } from "@/contexts/AuthContext"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ServerIcon, GlobeIcon, PlugIcon } from "lucide-react"

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

export function SettingsPage() {
  const { session } = useAuth()
  const [servers, setServers] = useState<Server[]>([])
  const [meta, setMeta] = useState<APIMeta | null>(null)
  const [publicUrlInput, setPublicUrlInput] = useState("")
  const [dashboardHostInput, setDashboardHostInput] = useState("")
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

  const load = () =>
    Promise.all([api.listServers(), api.getMeta(), api.listOrgProviderConnections()]).then(([s, m, p]) => {
      setServers(s)
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
      setDashboardHostInput(m.dashboard_public_host || "")
      setProviders(p as ProviderRow[])
    })

  useEffect(() => {
    load()
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [])

  const canManageProviders =
    session?.authenticated && (session.role === "owner" || session.role === "admin")

  const handleSavePublicURL = async () => {
    setSaving(true)
    setError(null)
    try {
      const m = await api.updateMeta({
        public_base_url: publicUrlInput.trim(),
        dashboard_public_host: dashboardHostInput.trim(),
      })
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
      setDashboardHostInput(m.dashboard_public_host || "")
    } catch (e) {
      setError(e instanceof APIError ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-6 max-w-5xl mx-auto w-full min-w-0">
      <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>

      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <Tabs defaultValue="public-url" className="min-w-0">
        <TabsList variant="line" className="w-full min-w-0 max-w-full justify-start overflow-x-auto">
          <TabsTrigger value="public-url" className="shrink-0">
            <GlobeIcon className="size-4" />
            Public URL
          </TabsTrigger>
          <TabsTrigger value="cluster" className="shrink-0">
            <ServerIcon className="size-4" />
            Cluster
          </TabsTrigger>
          <TabsTrigger value="providers" className="shrink-0">
            <PlugIcon className="size-4" />
            Providers
          </TabsTrigger>
        </TabsList>

        <TabsContent value="public-url" className="mt-4">
          <Card>
            <CardContent className="pt-6 space-y-4 text-sm">
              <p className="text-muted-foreground">
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
              <div className="flex flex-wrap items-center gap-2">
                <Button size="sm" onClick={() => void handleSavePublicURL()} disabled={saving}>
                  {saving ? "Saving…" : "Save"}
                </Button>
                {meta?.public_base_url_configured && (
                  <span className="text-xs text-muted-foreground">
                    Current: <code className="rounded bg-muted px-1 py-0.5 font-mono">{meta.public_base_url}</code>
                  </span>
                )}
              </div>
              <p className="text-xs text-muted-foreground">
                First-boot seeds: <span className="font-mono">KINDLING_PUBLIC_URL</span> /{" "}
                <span className="font-mono">--public-url</span>, and <span className="font-mono">KINDLING_DASHBOARD_HOST</span>{" "}
                / <span className="font-mono">--dashboard-host</span>, only when the corresponding row is missing.
                Production build: set <span className="font-mono">VITE_API_URL</span> to the API base URL.
              </p>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="providers" className="mt-4">
          <Card>
            <CardContent className="pt-6 space-y-4 text-sm">
              <p className="text-muted-foreground">
                GitHub / GitLab connections for this organization (metadata and encrypted tokens).
              </p>
              {providers.length === 0 ? (
                <p className="text-muted-foreground">No provider connections yet.</p>
              ) : (
                <ul className="space-y-2">
                  {providers.map((p) => (
                    <li
                      key={p.id}
                      className="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3"
                    >
                      <div>
                        <span className="font-medium">{p.display_label || p.external_slug}</span>
                        <span className="text-muted-foreground text-xs ml-2">{p.provider}</span>
                        {p.external_slug ? (
                          <span className="block text-xs text-muted-foreground font-mono">{p.external_slug}</span>
                        ) : null}
                        <span className="text-xs text-muted-foreground">
                          {p.has_credentials ? "credentials stored" : "no token"}
                        </span>
                      </div>
                      {canManageProviders ? (
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
                      ) : null}
                    </li>
                  ))}
                </ul>
              )}
              {canManageProviders ? (
                <div className="space-y-3 max-w-xl border rounded-lg p-4">
                  <p className="font-medium text-sm">Add connection</p>
                  <div className="space-y-2">
                    <Label>Provider</Label>
                    <select
                      className="w-full text-sm rounded-md border bg-background px-2 py-2"
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
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="cluster" className="mt-4">
          <Card>
            <CardContent className="pt-6">
              {servers.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4 text-center">No servers registered yet.</p>
              ) : (
                <div className="space-y-3">
                  {servers.map((server) => (
                    <div
                      key={server.id}
                      className="flex flex-col gap-2 rounded-lg border p-3 sm:flex-row sm:items-center sm:justify-between"
                    >
                      <div className="min-w-0">
                        <p className="font-mono text-sm font-medium truncate">{server.hostname || server.id}</p>
                        <p className="text-xs text-muted-foreground mt-0.5 truncate">
                          {server.internal_ip || "No IP"}
                          {server.instance_count != null && server.instance_count > 0
                            ? ` · ${server.instance_count} instance${server.instance_count === 1 ? "" : "s"}`
                            : ""}
                        </p>
                      </div>
                      <div className="flex flex-wrap items-center gap-2 sm:gap-3">
                        <span className="text-xs text-muted-foreground">
                          Last heartbeat: {new Date(server.last_heartbeat_at).toLocaleTimeString()}
                        </span>
                        <Badge variant={server.status === "active" ? "default" : "secondary"}>{server.status}</Badge>
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
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
