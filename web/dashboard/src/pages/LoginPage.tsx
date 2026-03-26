import { useEffect, useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { useAuth } from "@/contexts/AuthContext"
import { api } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function LoginPage() {
  const navigate = useNavigate()
  const { refresh } = useAuth()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [needsBootstrap, setNeedsBootstrap] = useState(false)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const s = await api.authBootstrapStatus()
        if (!cancelled) setNeedsBootstrap(s.needs_bootstrap)
      } catch {
        if (!cancelled) setNeedsBootstrap(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <div className="w-full max-w-sm space-y-6">
        <div className="space-y-2 text-center">
          <h1 className="text-2xl font-semibold tracking-tight">Sign in</h1>
          <p className="text-muted-foreground text-sm">Kindling control plane</p>
        </div>
        <form
          className="space-y-4"
          onSubmit={async (e) => {
            e.preventDefault()
            setError(null)
            setBusy(true)
            try {
              await api.authLogin({ email, password })
              await refresh()
              navigate("/", { replace: true })
            } catch (err) {
              setError(err instanceof Error ? err.message : "Login failed")
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="email">Email</Label>
            <Input
              id="email"
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">Password</Label>
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </div>
          {error ? <p className="text-destructive text-sm">{error}</p> : null}
          <Button type="submit" className="w-full" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
        {needsBootstrap ? (
          <p className="text-center text-sm text-muted-foreground">
            New cluster?{" "}
            <Link to="/bootstrap" className="text-primary underline">
              Create admin account
            </Link>
          </p>
        ) : null}
      </div>
    </div>
  )
}
