import { useEffect, useState } from "react"
import { api, type Server } from "@/lib/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { ServerIcon } from "lucide-react"

export function SettingsPage() {
  const [servers, setServers] = useState<Server[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    api.listServers()
      .then(setServers)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-32" />
        <Skeleton className="h-48 rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>

      {error && (
        <div className="rounded-xl border border-destructive/50 bg-destructive/5 p-4 text-destructive text-sm">
          {error}
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <ServerIcon className="size-4" />
            Cluster Servers
          </CardTitle>
        </CardHeader>
        <CardContent>
          {servers.length === 0 ? (
            <p className="text-sm text-muted-foreground py-4 text-center">
              No servers registered yet.
            </p>
          ) : (
            <div className="space-y-3">
              {servers.map((server) => (
                <div key={server.id} className="flex items-center justify-between rounded-lg border p-3">
                  <div>
                    <p className="font-mono text-sm font-medium">{server.hostname || server.id}</p>
                    <p className="text-xs text-muted-foreground mt-0.5">
                      {server.internal_ip || "No IP"}
                    </p>
                  </div>
                  <div className="flex items-center gap-3">
                    <span className="text-xs text-muted-foreground">
                      Last heartbeat: {new Date(server.last_heartbeat_at).toLocaleTimeString()}
                    </span>
                    <Badge variant={server.status === "active" ? "default" : "secondary"}>
                      {server.status}
                    </Badge>
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
