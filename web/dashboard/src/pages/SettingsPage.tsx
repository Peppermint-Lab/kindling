import { useEffect, useState } from "react"
import { api, type Server, type APIMeta, APIError } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { ServerIcon, GlobeIcon } from "lucide-react"

export function SettingsPage() {
  const [servers, setServers] = useState<Server[]>([])
  const [meta, setMeta] = useState<APIMeta | null>(null)
  const [publicUrlInput, setPublicUrlInput] = useState("")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = () =>
    Promise.all([api.listServers(), api.getMeta()]).then(([s, m]) => {
      setServers(s)
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
    })

  useEffect(() => {
    load()
      .catch((e) => setError(e instanceof APIError ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [])

  const handleSavePublicURL = async () => {
    setSaving(true)
    setError(null)
    try {
      const m = await api.updateMeta({ public_base_url: publicUrlInput.trim() })
      setMeta(m)
      setPublicUrlInput(m.public_base_url || "")
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
    <div className="space-y-6 max-w-5xl mx-auto w-full">
      <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>

      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <GlobeIcon className="size-4" />
            Public URL
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4 text-sm">
          <p className="text-muted-foreground">
            Stored in the database (<span className="font-mono">cluster_settings.public_base_url</span>). Used for
            absolute GitHub webhook links in the dashboard. Leave empty if you do not expose the API on a stable public
            URL yet.
          </p>
          <div className="space-y-2 max-w-xl">
            <Label htmlFor="public-url">Public base URL</Label>
            <Input
              id="public-url"
              placeholder="https://kindling.example.com"
              className="font-mono text-sm"
              value={publicUrlInput}
              onChange={(e) => setPublicUrlInput(e.target.value)}
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
            On first boot only, <span className="font-mono">KINDLING_PUBLIC_URL</span> or{" "}
            <span className="font-mono">--public-url</span> can seed this value if the setting row does not exist yet.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <ServerIcon className="size-4" />
            Cluster servers
          </CardTitle>
        </CardHeader>
        <CardContent>
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
                    </p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2 sm:gap-3">
                    <span className="text-xs text-muted-foreground">
                      Last heartbeat: {new Date(server.last_heartbeat_at).toLocaleTimeString()}
                    </span>
                    <Badge variant={server.status === "active" ? "default" : "secondary"}>{server.status}</Badge>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
