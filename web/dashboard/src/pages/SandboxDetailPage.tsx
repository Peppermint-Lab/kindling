import { useEffect, useMemo, useRef, useState } from "react"
import { useParams } from "react-router-dom"
import { API_BASE, api, type Sandbox, type SandboxAccessEvent } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

function sandboxShellWebSocketURL(id: string) {
  const base = new URL(API_BASE)
  base.protocol = base.protocol === "https:" ? "wss:" : "ws:"
  base.pathname = `/api/sandboxes/${id}/shell/ws`
  base.search = ""
  return base.toString()
}

function SandboxTerminal({ sandbox }: { sandbox: Sandbox }) {
  const [output, setOutput] = useState("")
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const shellRef = useRef<WebSocket | null>(null)
  const viewRef = useRef<HTMLDivElement | null>(null)

  const sendFrame = (frame: Record<string, unknown>) => {
    if (!shellRef.current || shellRef.current.readyState !== WebSocket.OPEN) return
    shellRef.current.send(JSON.stringify(frame))
  }

  const connect = () => {
    setError(null)
    const ws = new WebSocket(sandboxShellWebSocketURL(sandbox.id))
    shellRef.current = ws
    ws.onopen = () => {
      setConnected(true)
      sendFrame({ type: "resize", width: 120, height: 32 })
    }
    ws.onmessage = (event) => {
      try {
        const frame = JSON.parse(String(event.data)) as {
          type: string
          data?: string
          error?: string
          exit_code?: number
        }
        if ((frame.type === "stdout" || frame.type === "stderr") && frame.data) {
          setOutput((prev) => prev + frame.data)
          return
        }
        if (frame.type === "error" && frame.error) {
          setError(frame.error)
          return
        }
        if (frame.type === "exit") {
          setConnected(false)
        }
      } catch {
        setOutput((prev) => prev + String(event.data))
      }
    }
    ws.onerror = () => setError("Shell connection failed")
    ws.onclose = () => setConnected(false)
  }

  const disconnect = () => {
    shellRef.current?.close()
    shellRef.current = null
    setConnected(false)
  }

  useEffect(() => {
    if (!viewRef.current) return
    viewRef.current.scrollTop = viewRef.current.scrollHeight
  }, [output])

  useEffect(() => () => disconnect(), [])

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <div>
            <CardTitle>Shell</CardTitle>
            <CardDescription>WebSocket-backed live terminal using the sandbox guest agent.</CardDescription>
          </div>
          {connected ? (
            <Button variant="outline" onClick={disconnect}>Disconnect</Button>
          ) : (
            <Button onClick={connect}>Connect</Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div
          ref={viewRef}
          tabIndex={0}
          onKeyDown={(event) => {
            if (!connected) return
            if (event.ctrlKey && event.key.toLowerCase() === "c") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u0003" })
              return
            }
            if (event.key === "Enter") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\r" })
              return
            }
            if (event.key === "Backspace") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u007f" })
              return
            }
            if (event.key === "Tab") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\t" })
              return
            }
            if (event.key === "ArrowUp") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u001b[A" })
              return
            }
            if (event.key === "ArrowDown") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u001b[B" })
              return
            }
            if (event.key === "ArrowRight") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u001b[C" })
              return
            }
            if (event.key === "ArrowLeft") {
              event.preventDefault()
              sendFrame({ type: "stdin", data: "\u001b[D" })
              return
            }
            if (event.key.length === 1) {
              event.preventDefault()
              sendFrame({ type: "stdin", data: event.key })
            }
          }}
          className="min-h-80 overflow-auto rounded-lg border bg-black px-4 py-3 font-mono text-sm text-green-200 outline-none"
        >
          <pre className="whitespace-pre-wrap">{output || "Click Connect, then focus this terminal and start typing."}</pre>
        </div>
        {error ? <p className="text-sm text-destructive">{error}</p> : null}
      </CardContent>
    </Card>
  )
}

export function SandboxDetailPage() {
  const { id = "" } = useParams()
  const [sandbox, setSandbox] = useState<Sandbox | null>(null)
  const [logs, setLogs] = useState<string[]>([])
  const [stats, setStats] = useState<Record<string, unknown> | null>(null)
  const [events, setEvents] = useState<SandboxAccessEvent[]>([])
  const [targetPort, setTargetPort] = useState("3000")
  const [hostname, setHostname] = useState("")
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
            <CardDescription>Real SSH client access rides through the control plane proxy.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <pre className="overflow-x-auto rounded-md bg-muted/50 p-3 font-mono text-xs">{sshCommand}</pre>
            <p className="text-xs text-muted-foreground">Add keys under Settings → SSH Keys, then use the CLI command above.</p>
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
