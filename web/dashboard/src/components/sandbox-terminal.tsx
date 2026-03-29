import { useEffect, useRef, useState } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

import { API_BASE, type Sandbox } from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"

function sandboxShellWebSocketURL(id: string) {
  const base = new URL(API_BASE)
  base.protocol = base.protocol === "https:" ? "wss:" : "ws:"
  base.pathname = `/api/sandboxes/${id}/shell/ws`
  base.search = ""
  return base.toString()
}

export function SandboxTerminal({ sandbox }: { sandbox: Sandbox }) {
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const resizeObserverRef = useRef<ResizeObserver | null>(null)

  useEffect(() => {
    if (!containerRef.current) return

    const terminal = new Terminal({
      cursorBlink: true,
      fontFamily: '"Geist Mono Variable", "SFMono-Regular", ui-monospace, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 5000,
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
    terminal.writeln("Kindling sandbox terminal ready.")
    terminal.writeln("Click Connect to attach to the guest shell.")

    terminalRef.current = terminal
    fitRef.current = fitAddon

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
      socketRef.current?.close()
      socketRef.current = null
      terminal.dispose()
      terminalRef.current = null
      fitRef.current = null
    }
  }, [])

  const connect = () => {
    const terminal = terminalRef.current
    const fitAddon = fitRef.current
    if (!terminal || !fitAddon) return

    socketRef.current?.close()
    setError(null)
    terminal.clear()
    terminal.writeln("Connecting to sandbox shell...")

    const socket = new WebSocket(sandboxShellWebSocketURL(sandbox.id))
    socketRef.current = socket

    socket.onopen = () => {
      setConnected(true)
      fitAddon.fit()
      socket.send(JSON.stringify({ type: "resize", width: terminal.cols, height: terminal.rows }))
    }

    socket.onmessage = (event) => {
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
              setError(frame.error)
              terminal.writeln(`\r\n[error] ${frame.error}`)
            }
            return
          case "exit":
            setConnected(false)
            terminal.writeln(`\r\n[session ended${typeof frame.exit_code === "number" ? `, exit ${frame.exit_code}` : ""}]`)
            return
          default:
            return
        }
      } catch {
        terminal.write(String(event.data))
      }
    }

    socket.onerror = () => {
      setError("Shell connection failed")
      terminal.writeln("\r\n[connection failed]")
    }

    socket.onclose = () => {
      setConnected(false)
    }
  }

  const disconnect = () => {
    socketRef.current?.close()
    socketRef.current = null
    setConnected(false)
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <div>
            <CardTitle>Shell</CardTitle>
            <CardDescription>Real terminal emulation over the sandbox WebSocket shell transport.</CardDescription>
          </div>
          {connected ? (
            <Button variant="outline" onClick={disconnect}>
              Disconnect
            </Button>
          ) : (
            <Button onClick={connect}>Connect</Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="rounded-xl border bg-[#050816] p-2">
          <div ref={containerRef} className="h-[28rem] w-full overflow-hidden rounded-lg" />
        </div>
        {error ? <p className="text-sm text-destructive">{error}</p> : null}
      </CardContent>
    </Card>
  )
}
