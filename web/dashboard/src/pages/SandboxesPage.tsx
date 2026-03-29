import { useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { api, type Sandbox } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export function SandboxesPage() {
  const [sandboxes, setSandboxes] = useState<Sandbox[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setSandboxes(await api.listSandboxes())
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load sandboxes")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Sandboxes</h1>
          <p className="text-sm text-muted-foreground">Persistent and disposable microVM workspaces across Linux and macOS workers.</p>
        </div>
        <Button variant="outline" onClick={() => void load()} disabled={loading}>
          Refresh
        </Button>
      </div>

      {error ? (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : null}

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {sandboxes.map((sandbox) => (
          <Link key={sandbox.id} to={`/sandboxes/${sandbox.id}`} className="block">
            <Card className="h-full transition-colors hover:border-foreground/30">
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <CardTitle>{sandbox.name}</CardTitle>
                    <CardDescription>{sandbox.host_group}</CardDescription>
                  </div>
                  <Badge variant={sandbox.observed_state === "running" ? "default" : "secondary"}>
                    {sandbox.observed_state}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>{sandbox.backend || "pending backend"} / {sandbox.arch || "pending arch"}</p>
                <p>{sandbox.vcpu} vCPU, {sandbox.memory_mb} MB RAM, {sandbox.disk_gb} GB disk</p>
                <p className="truncate">{sandbox.runtime_url || "No runtime URL yet"}</p>
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>

      {!loading && sandboxes.length === 0 ? (
        <Card>
          <CardContent className="pt-6 text-sm text-muted-foreground">No sandboxes yet in this organization.</CardContent>
        </Card>
      ) : null}
    </div>
  )
}
