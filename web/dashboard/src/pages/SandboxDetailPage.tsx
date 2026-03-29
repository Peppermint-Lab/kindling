import { useEffect, useMemo, useState } from "react"
import { useParams } from "react-router-dom"
import { CopyIcon, ShieldCheckIcon } from "lucide-react"
import { api, type Sandbox, type SandboxAccessEvent } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SandboxTerminal } from "@/components/sandbox-terminal"

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    console.warn("clipboard failed", label)
  }
}

function decodeOpenSSHPublicKey(publicKey: string): Uint8Array | null {
  const parts = publicKey.trim().split(/\s+/)
  if (parts.length < 2) return null
  try {
    return Uint8Array.from(atob(parts[1]), (char) => char.charCodeAt(0))
  } catch {
    return null
  }
}

async function sshPublicKeyFingerprint(publicKey: string): Promise<string | null> {
  const bytes = decodeOpenSSHPublicKey(publicKey)
  if (!bytes) return null
  const digestInput =
    bytes.buffer instanceof ArrayBuffer
      ? bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
      : new Uint8Array(bytes).buffer
  const digest = await crypto.subtle.digest("SHA-256", digestInput)
  const encoded = btoa(String.fromCharCode(...new Uint8Array(digest))).replace(/=+$/, "")
  return `SHA256:${encoded}`
}

function compactSSHKey(publicKey: string): string {
  const parts = publicKey.trim().split(/\s+/)
  if (parts.length < 2) return publicKey.trim()
  const body = parts[1]
  if (body.length <= 24) return `${parts[0]} ${body}`
  return `${parts[0]} ${body.slice(0, 16)}...${body.slice(-8)}`
}

export function SandboxDetailPage() {
  const { id = "" } = useParams()
  const [sandbox, setSandbox] = useState<Sandbox | null>(null)
  const [logs, setLogs] = useState<string[]>([])
  const [stats, setStats] = useState<Record<string, unknown> | null>(null)
  const [events, setEvents] = useState<SandboxAccessEvent[]>([])
  const [targetPort, setTargetPort] = useState("3000")
  const [hostname, setHostname] = useState("")
  const [sshFingerprint, setSSHFingerprint] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setError(null)
    try {
      const [sandboxValue, logsValue, statsValue, eventsValue] = await Promise.all([
        api.getSandbox(id),
        api.getSandboxLogs(id),
        api.getSandboxStats(id),
        api.getSandboxAccessEvents(id),
      ])
      setSandbox(sandboxValue)
      setLogs(logsValue)
      setStats(statsValue)
      setEvents(eventsValue)
      setSSHFingerprint(sandboxValue.ssh_host_public_key ? await sshPublicKeyFingerprint(sandboxValue.ssh_host_public_key) : null)
      setTargetPort(String(sandboxValue.published_http_port ?? sandboxValue.published_ports?.[0]?.target_port ?? 3000))
      setHostname(sandboxValue.published_ports?.[0]?.public_hostname ?? "")
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load sandbox")
    }
  }

  useEffect(() => {
    void load()
  }, [id])

  const sshCommand = useMemo(() => `kindling sandbox ssh --sandbox ${id}`, [id])
  const sshHostKeySummary = sandbox?.ssh_host_public_key ? compactSSHKey(sandbox.ssh_host_public_key) : null

  if (!sandbox) {
    return (
      <Card>
        <CardContent className="pt-6 text-sm text-muted-foreground">{error || "Loading sandbox…"}</CardContent>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">{sandbox.name}</h1>
            <Badge variant={sandbox.observed_state === "running" ? "default" : "secondary"}>{sandbox.observed_state}</Badge>
          </div>
          <p className="text-sm text-muted-foreground">{sandbox.host_group} · {sandbox.backend || "pending"} · {sandbox.arch || "pending"}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {(["start", "stop", "suspend", "resume"] as const).map((action) => (
            <Button key={action} variant="outline" onClick={() => void api.sandboxAction(id, action).then(load)}>
              {action}
            </Button>
          ))}
        </div>
      </div>

      {error ? (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>Runtime</CardTitle>
            <CardDescription>Current placement, resources, and runtime address.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-2 text-sm">
            <p><span className="font-medium">Runtime URL:</span> {sandbox.runtime_url || "—"}</p>
            <p><span className="font-medium">Resources:</span> {sandbox.vcpu} vCPU / {sandbox.memory_mb} MB / {sandbox.disk_gb} GB</p>
            <p><span className="font-medium">Git:</span> {sandbox.git_repo || "—"} {sandbox.git_ref ? `(${sandbox.git_ref})` : ""}</p>
            <p><span className="font-medium">Last used:</span> {sandbox.last_used_at ? new Date(sandbox.last_used_at).toLocaleString() : "—"}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>SSH</CardTitle>
            <CardDescription>Real SSH client access rides through the control plane proxy with a managed host key.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <pre className="overflow-x-auto rounded-md bg-muted/50 p-3 font-mono text-xs">{sshCommand}</pre>
            <p className="text-xs text-muted-foreground">Add keys under Settings → SSH Keys, then use the CLI command above.</p>
            {sandbox.ssh_host_public_key ? (
              <div className="space-y-3 rounded-lg border bg-muted/20 p-3">
                <div className="flex items-center gap-2 text-sm font-medium">
                  <ShieldCheckIcon className="size-4 text-muted-foreground" />
                  Managed Host Trust
                </div>
                <div className="space-y-1">
                  <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Fingerprint</p>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code className="flex-1 rounded-md border bg-background px-3 py-2 font-mono text-xs break-all">
                      {sshFingerprint || "Calculating…"}
                    </code>
                    <Button type="button" variant="outline" size="sm" onClick={() => sshFingerprint && void copyText("sandbox-ssh-fingerprint", sshFingerprint)} disabled={!sshFingerprint}>
                      <CopyIcon className="mr-2 size-3" />
                      Copy
                    </Button>
                  </div>
                </div>
                <div className="space-y-1">
                  <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Host Key</p>
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code className="flex-1 rounded-md border bg-background px-3 py-2 font-mono text-xs break-all">
                      {sshHostKeySummary}
                    </code>
                    <Button type="button" variant="outline" size="sm" onClick={() => void copyText("sandbox-ssh-host-key", sandbox.ssh_host_public_key ?? "")}>
                      <CopyIcon className="mr-2 size-3" />
                      Copy
                    </Button>
                  </div>
                </div>
                <p className="text-xs text-muted-foreground">Kindling records this sandbox's host key and the CLI verifies it before connecting.</p>
              </div>
            ) : (
              <div className="rounded-lg border border-dashed p-3 text-xs text-muted-foreground">
                Host key not ready yet. Start the sandbox and ensure the guest image includes `sshd` and `ssh-keygen`.
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>HTTP Publish</CardTitle>
          <CardDescription>Expose a stable managed hostname for an HTTP port inside this sandbox.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-[160px_1fr_auto_auto]">
          <div className="space-y-2">
            <Label htmlFor="sandbox-port">Target Port</Label>
            <Input id="sandbox-port" value={targetPort} onChange={(e) => setTargetPort(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="sandbox-hostname">Hostname</Label>
            <Input id="sandbox-hostname" value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="auto-generated if blank" />
          </div>
          <Button
            className="self-end"
            onClick={() => void api.publishSandboxHTTP(id, Number(targetPort || "0"), hostname || undefined).then(load)}
          >
            Publish
          </Button>
          <Button className="self-end" variant="outline" onClick={() => void api.unpublishSandboxHTTP(id).then(load)}>
            Unpublish
          </Button>
        </CardContent>
      </Card>

      <SandboxTerminal sandbox={sandbox} />

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Logs</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="max-h-80 overflow-auto rounded-md bg-muted/50 p-4 font-mono text-xs whitespace-pre-wrap">{logs.join("\n") || "No logs yet."}</pre>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Stats</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="overflow-auto rounded-md bg-muted/50 p-4 font-mono text-xs whitespace-pre-wrap">{JSON.stringify(stats, null, 2)}</pre>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Access History</CardTitle>
          <CardDescription>Recent shell, SSH, exec, and file transfer activity for this sandbox.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {events.map((event) => (
            <div key={event.id} className="rounded-lg border p-3 text-sm">
              <div className="flex items-center justify-between gap-3">
                <p className="font-medium">{event.access_method} · {event.event_type}</p>
                <p className="text-xs text-muted-foreground">{new Date(event.created_at).toLocaleString()}</p>
              </div>
              <p className="text-xs text-muted-foreground">{event.display_name || event.user_email || "Unknown user"}</p>
              {event.error_summary ? <p className="mt-2 text-xs text-destructive">{event.error_summary}</p> : null}
            </div>
          ))}
          {events.length === 0 ? <p className="text-sm text-muted-foreground">No access events yet.</p> : null}
        </CardContent>
      </Card>
    </div>
  )
}
