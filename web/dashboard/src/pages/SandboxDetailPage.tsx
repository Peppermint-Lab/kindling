import { useEffect, useMemo, useState } from "react"
import { useNavigate, useParams } from "react-router-dom"
import { AlertTriangleIcon, CopyIcon, RotateCcwIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react"
import { api, type Sandbox, type SandboxAccessEvent } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SandboxTerminal } from "@/components/sandbox-terminal"
import { sandboxImageContract } from "@/lib/sandbox-catalog"

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
  const navigate = useNavigate()
  const { id = "" } = useParams()
  const [sandbox, setSandbox] = useState<Sandbox | null>(null)
  const [logs, setLogs] = useState<string[]>([])
  const [stats, setStats] = useState<Record<string, unknown> | null>(null)
  const [events, setEvents] = useState<SandboxAccessEvent[]>([])
  const [targetPort, setTargetPort] = useState("3000")
  const [hostname, setHostname] = useState("")
  const [baseImageRef, setBaseImageRef] = useState("")
  const [vcpu, setVcpu] = useState("2")
  const [memoryMb, setMemoryMb] = useState("2048")
  const [diskGb, setDiskGb] = useState("10")
  const [autoSuspendEnabled, setAutoSuspendEnabled] = useState(false)
  const [autoSuspendSeconds, setAutoSuspendSeconds] = useState("900")
  const [templateName, setTemplateName] = useState("")
  const [notice, setNotice] = useState<string | null>(null)
  const [sshFingerprint, setSSHFingerprint] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setError(null)
    try {
      const sandboxValue = await api.getSandbox(id)
      const canLoadObservability = sandboxValue.observed_state === "running"
      const [logsValue, statsValue, eventsValue] = await Promise.all([
        canLoadObservability ? api.getSandboxLogs(id).catch(() => []) : Promise.resolve([]),
        canLoadObservability ? api.getSandboxStats(id).catch(() => null) : Promise.resolve(null),
        api.getSandboxAccessEvents(id).catch(() => []),
      ])
      setSandbox(sandboxValue)
      setLogs(logsValue)
      setStats(statsValue)
      setEvents(eventsValue)
      setSSHFingerprint(sandboxValue.ssh_host_public_key ? await sshPublicKeyFingerprint(sandboxValue.ssh_host_public_key) : null)
      setTargetPort(String(sandboxValue.published_http_port ?? sandboxValue.published_ports?.[0]?.target_port ?? 3000))
      setHostname(sandboxValue.published_ports?.[0]?.public_hostname ?? "")
      setBaseImageRef(sandboxValue.base_image_ref)
      setVcpu(String(sandboxValue.vcpu))
      setMemoryMb(String(sandboxValue.memory_mb))
      setDiskGb(String(sandboxValue.disk_gb))
      setAutoSuspendEnabled(sandboxValue.auto_suspend_seconds > 0)
      setAutoSuspendSeconds(String(sandboxValue.auto_suspend_seconds > 0 ? sandboxValue.auto_suspend_seconds : 900))
      setTemplateName(`${sandboxValue.name}-template`)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load remote VM")
    }
  }

  useEffect(() => {
    void load()
  }, [id])

  const sshCommand = useMemo(() => `kindling vm ssh --vm ${id}`, [id])
  const sshHostKeySummary = sandbox?.ssh_host_public_key ? compactSSHKey(sandbox.ssh_host_public_key) : null
  const imageContract = sandbox ? sandboxImageContract(baseImageRef || sandbox.base_image_ref) : null
  const canEditSandboxConfig = sandbox?.observed_state !== "running"

  if (!sandbox) {
    return (
      <Card>
        <CardContent className="pt-6 text-sm text-muted-foreground">{error || "Loading remote VM…"}</CardContent>
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
          <Button variant="outline" onClick={() => void api.sandboxAction(id, "stop").then(() => api.sandboxAction(id, "start")).then(load)}>
            <RotateCcwIcon className="mr-2 size-4" />
            Restart
          </Button>
          <Button
            variant="destructive"
            onClick={() => {
              if (!window.confirm("Delete this remote VM?")) return
              void api.deleteSandbox(id).then(() => navigate("/vms"))
            }}
          >
            <Trash2Icon className="mr-2 size-4" />
            Delete
          </Button>
        </div>
      </div>

      {error ? (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{error}</CardContent>
        </Card>
      ) : null}

      {notice ? (
        <Card>
          <CardContent className="pt-6 text-sm text-foreground">{notice}</CardContent>
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
            <p><span className="font-medium">Auto-suspend:</span> {sandbox.auto_suspend_seconds > 0 ? `${sandbox.auto_suspend_seconds}s` : "Always on"}</p>
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
                <p className="text-xs text-muted-foreground">Kindling records this remote VM's host key and the CLI verifies it before connecting.</p>
              </div>
            ) : (
              <div className="rounded-lg border border-dashed p-3 text-xs text-muted-foreground">
                Host key not ready yet. Start the remote VM and ensure the guest image includes `sshd` and `ssh-keygen`.
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Lifecycle</CardTitle>
          <CardDescription>Remote VMs are always on by default. Opt into idle auto-suspend only when you want it.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-[auto_160px_auto] md:items-end">
          <label className="flex items-start gap-3 text-sm">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={autoSuspendEnabled}
              onChange={(e) => setAutoSuspendEnabled(e.target.checked)}
            />
            <span>
              <span className="block font-medium">Enable auto-suspend</span>
              <span className="block text-muted-foreground">When disabled, the remote VM stays on until you stop or delete it.</span>
            </span>
          </label>
          <div className="space-y-2">
            <Label htmlFor="sandbox-auto-suspend">Idle timeout (seconds)</Label>
            <Input
              id="sandbox-auto-suspend"
              type="number"
              min="1"
              step="1"
              value={autoSuspendSeconds}
              disabled={!autoSuspendEnabled}
              onChange={(e) => setAutoSuspendSeconds(e.target.value)}
            />
          </div>
          <Button
            className="self-end"
            onClick={() => {
              const next = autoSuspendEnabled ? Number(autoSuspendSeconds || "0") : 0
              if (autoSuspendEnabled && (!Number.isFinite(next) || next <= 0)) {
                setError("Auto-suspend timeout must be a positive number of seconds")
                return
              }
              void api.updateSandbox(id, { auto_suspend_seconds: next }).then(load)
            }}
          >
            Save Lifecycle
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Templates</CardTitle>
          <CardDescription>Capture this remote VM into a reusable template. Kindling will stop it if needed, snapshot it, then make it available from the Remote VMs page.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-[1fr_auto] md:items-end">
          <div className="space-y-2">
            <Label htmlFor="sandbox-template-name">Template Name</Label>
            <Input
              id="sandbox-template-name"
              value={templateName}
              onChange={(e) => setTemplateName(e.target.value)}
              placeholder={`${sandbox.name}-template`}
            />
            <p className="text-xs text-muted-foreground">
              {sandbox.observed_state === "running"
                ? "The remote VM is running now, so capture will stop it before creating the snapshot."
                : "Stopped remote VMs usually become ready faster because they can be snapshotted immediately."}
            </p>
          </div>
          <Button
            variant="outline"
            onClick={() => {
              setError(null)
              setNotice(null)
              void api.createSandboxTemplate(id, { name: templateName.trim() || undefined }).then((template) => {
                setNotice(`Template capture started for ${template.name}. It will show up on the Remote VMs page as it moves to ready.`)
                void load()
              }).catch((err: unknown) => {
                setError(err instanceof Error ? err.message : "Failed to start template capture")
              })
            }}
          >
            Capture Template
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Guest Image</CardTitle>
          <CardDescription>Kindling expects interactive remote VM images to include `/bin/sh`, and SSH access additionally depends on `sshd` and `ssh-keygen`.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className={`rounded-lg border p-3 text-sm ${imageContract?.tone === "caution" ? "border-amber-400/50 bg-amber-500/10" : "bg-muted/20"}`}>
            <div className="flex items-center gap-2 font-medium">
              {imageContract?.tone === "caution" ? <AlertTriangleIcon className="size-4 text-amber-700" /> : <ShieldCheckIcon className="size-4 text-muted-foreground" />}
              {imageContract?.title}
            </div>
            <p className="mt-1 text-muted-foreground">{imageContract?.description}</p>
          </div>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <div className="space-y-2 xl:col-span-2">
              <Label htmlFor="sandbox-image-ref">Base Image</Label>
              <Input
                id="sandbox-image-ref"
                value={baseImageRef}
                disabled={!canEditSandboxConfig}
                onChange={(e) => setBaseImageRef(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="sandbox-vcpu">vCPU</Label>
              <Input
                id="sandbox-vcpu"
                type="number"
                min="1"
                step="1"
                value={vcpu}
                disabled={!canEditSandboxConfig}
                onChange={(e) => setVcpu(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="sandbox-memory">Memory (MB)</Label>
              <Input
                id="sandbox-memory"
                type="number"
                min="128"
                step="128"
                value={memoryMb}
                disabled={!canEditSandboxConfig}
                onChange={(e) => setMemoryMb(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="sandbox-disk">Disk (GB)</Label>
              <Input
                id="sandbox-disk"
                type="number"
                min="1"
                step="1"
                value={diskGb}
                disabled={!canEditSandboxConfig}
                onChange={(e) => setDiskGb(e.target.value)}
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-xs text-muted-foreground">
              {canEditSandboxConfig
                ? "These changes apply the next time the remote VM starts."
                : "Stop the remote VM before editing its base image or resources."}
            </p>
            <Button
              variant="outline"
              disabled={!canEditSandboxConfig}
              onClick={() => {
                const nextVcpu = Number(vcpu || "0")
                const nextMemoryMb = Number(memoryMb || "0")
                const nextDiskGb = Number(diskGb || "0")
                if (!baseImageRef.trim()) {
                  setError("Base image is required")
                  return
                }
                if (!Number.isFinite(nextVcpu) || nextVcpu <= 0 || !Number.isFinite(nextMemoryMb) || nextMemoryMb <= 0 || !Number.isFinite(nextDiskGb) || nextDiskGb <= 0) {
                  setError("Image and resource values must be valid positive numbers")
                  return
                }
                void api.updateSandbox(id, {
                  base_image_ref: baseImageRef.trim(),
                  vcpu: nextVcpu,
                  memory_mb: nextMemoryMb,
                  disk_gb: nextDiskGb,
                }).then(load)
              }}
            >
              Save Guest Config
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>HTTP Publish</CardTitle>
          <CardDescription>Expose a stable managed hostname for an HTTP port inside this remote VM.</CardDescription>
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
            <pre className="overflow-auto rounded-md bg-muted/50 p-4 font-mono text-xs whitespace-pre-wrap">
              {stats ? JSON.stringify(stats, null, 2) : "No stats yet."}
            </pre>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Access History</CardTitle>
          <CardDescription>Recent shell, SSH, exec, and file transfer activity for this remote VM.</CardDescription>
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
