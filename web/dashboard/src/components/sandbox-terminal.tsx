import { useEffect, useRef, useState } from "react"
import { Link } from "react-router-dom"
import { RotateCcwIcon } from "lucide-react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { API_BASE, type Sandbox } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  humanizeShellConnectionError,
  terminalShellAccessStatus,
  type AccessSurfaceStatus,
} from "@/lib/remote-vm-access"

function shellBadgeVariant(status: AccessSurfaceStatus): "default" | "secondary" | "outline" {
  if (status === "ready") return "default"
  if (status === "blocked") return "secondary"
  return "outline"
}

function shellBadgeLabel(status: AccessSurfaceStatus): string {
  if (status === "ready") return "Available"
  if (status === "blocked") return "Unavailable"
  return "Not supported"
}

function sandboxShellWebSocketURL(id: string) {
  const base = new URL(API_BASE)
  base.protocol = base.protocol === "https:" ? "wss:" : "ws:"
  base.pathname = `/api/vms/${id}/shell/ws`
  base.search = ""
  return base.toString()
}

type SessionPhase = "idle" | "connecting" | "connected"

export function SandboxTerminal({ sandbox }: { sandbox: Sandbox }) {
  const shellAccess = terminalShellAccessStatus(sandbox)
  const canConnect = shellAccess.status === "ready"

  const [phase, setPhase] = useState<SessionPhase>("idle")
  const [bannerError, setBannerError] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const connectionTokenRef = useRef(0)

  const resetTerminalView = () => {
    const terminal = terminalRef.current
    if (!terminal) return
    terminal.clear()
    terminal.writeln("Kindling remote VM shell")
    terminal.writeln("Connect when the VM is running to attach to the guest PTY.")
  }

  const closeActiveSocket = () => {
    connectionTokenRef.current += 1
    const activeSocket = socketRef.current
    socketRef.current = null
    if (terminalRef.current) terminalRef.current.options.disableStdin = true
    activeSocket?.close()
  }

  useEffect(() => {
    if (!containerRef.current) return

    const terminal = new Terminal({
      cursorBlink: true,
      fontFamily: '"Geist Mono Variable", "SFMono-Regular", ui-monospace, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 5000,
      disableStdin: true,
      theme: {
        background: "#050816",
        foreground: "#d9f99d",
        cursor: "#fef08a",
        selectionBackground: "#1e293b",
        black: "#020617",
        red: "#f87171",
        green: "#4ade80",
        yellow: "#facc15",
        blue: "#60a5fa",
        magenta: "#f472b6",
        cyan: "#22d3ee",
        white: "#e2e8f0",
        brightBlack: "#334155",
        brightRed: "#fb7185",
        brightGreen: "#86efac",
        brightYellow: "#fde047",
        brightBlue: "#93c5fd",
        brightMagenta: "#f9a8d4",
        brightCyan: "#67e8f9",
        brightWhite: "#f8fafc",
      },
    })
    const fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    terminal.open(containerRef.current)
    fitAddon.fit()
    terminalRef.current = terminal
    fitRef.current = fitAddon
    resetTerminalView()

    const resizeTerminal = () => {
      fitAddon.fit()
      if (socketRef.current?.readyState === WebSocket.OPEN) {
        socketRef.current.send(
          JSON.stringify({ type: "resize", width: terminal.cols, height: terminal.rows }),
        )
      }
    }

    const observer = new ResizeObserver(() => resizeTerminal())
    observer.observe(containerRef.current)
    resizeObserverRef.current = observer
    window.addEventListener("resize", resizeTerminal)

    const disposable = terminal.onData((data) => {
      if (socketRef.current?.readyState !== WebSocket.OPEN) return
      socketRef.current.send(JSON.stringify({ type: "stdin", data }))
    })

    return () => {
      disposable.dispose()
      observer.disconnect()
      resizeObserverRef.current = null
      window.removeEventListener("resize", resizeTerminal)
      closeActiveSocket()
      terminal.dispose()
      terminalRef.current = null
      fitRef.current = null
    }
  }, [])

  useEffect(() => {
    if (sandbox.observed_state !== "running") {
      const hadLiveSession =
        socketRef.current?.readyState === WebSocket.OPEN || socketRef.current?.readyState === WebSocket.CONNECTING
      closeActiveSocket()
      setPhase("idle")
      if (hadLiveSession) {
        const t = terminalRef.current
        if (t) {
          t.writeln("\r\n\x1b[33m[VM is not running — shell session closed]\x1b[0m")
        }
      }
    }
  }, [sandbox.observed_state, sandbox.id])

  useEffect(() => {
    closeActiveSocket()
    setPhase("idle")
    setBannerError(null)
    resetTerminalView()
  }, [sandbox.id])

  const disconnect = () => {
    closeActiveSocket()
    setPhase("idle")
  }

  const connect = () => {
    const terminal = terminalRef.current
    const fitAddon = fitRef.current
    if (!terminal || !fitAddon || !canConnect) return

    closeActiveSocket()
    setBannerError(null)
    setPhase("connecting")
    terminal.clear()
    terminal.writeln("\x1b[36mConnecting…\x1b[0m")

    connectionTokenRef.current += 1
    const token = connectionTokenRef.current
    const socket = new WebSocket(sandboxShellWebSocketURL(sandbox.id))
    let didOpen = false
    socketRef.current = socket

    socket.onopen = () => {
      if (connectionTokenRef.current !== token || socketRef.current !== socket) return
      didOpen = true
      setPhase("connected")
      terminal.options.disableStdin = false
      fitAddon.fit()
      socket.send(JSON.stringify({ type: "resize", width: terminal.cols, height: terminal.rows }))
    }

    socket.onmessage = (event) => {
      if (connectionTokenRef.current !== token || socketRef.current !== socket) return
      try {
        const frame = JSON.parse(String(event.data)) as {
          type: string
          data?: string
          error?: string
          exit_code?: number
        }
        switch (frame.type) {
          case "stdout":
          case "stderr":
            if (frame.data) terminal.write(frame.data)
            return
          case "error":
            if (frame.error) {
              const msg = humanizeShellConnectionError(frame.error)
              setBannerError(msg)
              terminal.writeln(`\r\n\x1b[31m${msg}\x1b[0m`)
            }
            return
          case "exit":
            terminal.options.disableStdin = true
            terminal.writeln(
              `\r\n\x1b[33m[Session ended${typeof frame.exit_code === "number" ? `, exit ${frame.exit_code}` : ""}]\x1b[0m`,
            )
            closeActiveSocket()
            setPhase("idle")
            return
          default:
            return
        }
      } catch {
        terminal.write(String(event.data))
      }
    }

    socket.onerror = () => {
      if (connectionTokenRef.current !== token || socketRef.current !== socket) return
      const msg = "Could not open the shell WebSocket. Check network, auth, and that the API allows WebSockets."
      setBannerError(humanizeShellConnectionError(msg))
      terminal.writeln(`\r\n\x1b[31m${msg}\x1b[0m`)
    }

    socket.onclose = (ev) => {
      if (connectionTokenRef.current !== token || socketRef.current !== socket) return
      socketRef.current = null
      terminal.options.disableStdin = true
      if (!didOpen) {
        const reason =
          ev.reason?.trim() ||
          (ev.code !== 1000 ? `Connection closed (${ev.code}).` : "Connection closed before the shell started.")
        const msg = humanizeShellConnectionError(reason)
        setBannerError(msg)
        terminal.writeln(`\r\n\x1b[31m${msg}\x1b[0m`)
      } else if (ev.code !== 1000) {
        const msg = humanizeShellConnectionError(ev.reason || `Connection lost (code ${ev.code}).`)
        setBannerError(msg)
        terminal.writeln(`\r\n\x1b[31m${msg}\x1b[0m`)
      }

      setPhase("idle")
    }
  }

  const connected = phase === "connected"
  const busy = phase === "connecting"

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle>Shell</CardTitle>
              <Badge variant={shellBadgeVariant(shellAccess.status)}>
                {shellBadgeLabel(shellAccess.status)}
              </Badge>
            </div>
            <CardDescription>{shellAccess.hint}</CardDescription>
          </div>
          <div className="flex flex-wrap gap-2">
            {connected ? (
              <Button variant="outline" onClick={disconnect}>
                Disconnect
              </Button>
            ) : (
              <>
                <Button onClick={connect} disabled={!canConnect || busy}>
                  {busy ? "Connecting…" : "Connect"}
                </Button>
                {bannerError && canConnect ? (
                  <Button variant="outline" onClick={connect} disabled={busy}>
                    <RotateCcwIcon className="mr-2 size-4" />
                    Reconnect
                  </Button>
                ) : null}
              </>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {!canConnect ? (
          <p className="text-sm text-muted-foreground">
            {shellAccess.status === "unsupported" ? (
              <>Use <Link to="/settings/ssh-keys" className="underline underline-offset-4">SSH</Link> or the CLI if your backend does not expose dashboard shell.</>
            ) : (
              <>Start the VM to enable shell. For SSH from your machine, see <strong>Connect</strong> above or{" "}
                <Link to="/settings/ssh-keys" className="underline underline-offset-4">SSH keys</Link>.</>
            )}
          </p>
        ) : null}
        {bannerError ? <p className="text-sm text-destructive">{bannerError}</p> : null}
        <div className="rounded-xl border bg-[#050816] p-2">
          <div ref={containerRef} className="h-[28rem] w-full overflow-hidden rounded-lg" />
        </div>
      </CardContent>
    </Card>
  )
}
