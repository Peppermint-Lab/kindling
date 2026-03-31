import { useEffect, useState } from "react"
import { useNavigate } from "react-router-dom"
import { useAuth } from "@/contexts/AuthContext"
import { api } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function BootstrapPage() {
  const navigate = useNavigate()
  const { refresh } = useAuth()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [displayName, setDisplayName] = useState("")
  const [bootstrapToken, setBootstrapToken] = useState("")
  const [bootstrapTokenConfigured, setBootstrapTokenConfigured] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const s = await api.authBootstrapStatus()
        if (!cancelled && !s.needs_bootstrap) {
          navigate("/login", { replace: true })
        }
        if (!cancelled) {
          setBootstrapTokenConfigured(s.bootstrap_token_configured)
        }
      } catch {
        /* stay on page */
      }
    })()
    return () => {
      cancelled = true
    }
  }, [navigate])

  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <div className="w-full max-w-sm space-y-6">
        <div className="space-y-2 text-center">
          <h1 className="text-2xl font-semibold tracking-tight">Create admin</h1>
          <p className="text-muted-foreground text-sm">
            First user becomes platform admin and owner of the default organization.
          </p>
          <p className="text-muted-foreground text-xs">
            Remote bootstrap requires a server bootstrap token. Local bootstrap works from loopback.
          </p>
        </div>
        <form
          className="space-y-4"
          onSubmit={async (e) => {
            e.preventDefault()
            setError(null)
            setBusy(true)
            try {
              const sess = await api.authBootstrap({
                email,
                password,
                display_name: displayName || undefined,
                bootstrap_token: bootstrapToken || undefined,
              })
              await refresh()
              navigate(
                sess.authenticated && sess.needs_onboarding ? "/onboarding" : "/",
                { replace: true },
              )
            } catch (err) {
              setError(err instanceof Error ? err.message : "Bootstrap failed")
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="b-email">Email</Label>
            <Input
              id="b-email"
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="b-password">Password</Label>
            <Input
              id="b-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              minLength={8}
            />
            <p className="text-muted-foreground text-xs">At least 8 characters.</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="b-name">Display name (optional)</Label>
            <Input
              id="b-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="b-token">
              Bootstrap token {bootstrapTokenConfigured ? "(required for remote setup)" : "(optional)"}
            </Label>
            <Input
              id="b-token"
              type="password"
              autoComplete="one-time-code"
              value={bootstrapToken}
              onChange={(e) => setBootstrapToken(e.target.value)}
            />
          </div>
          {error ? <p className="text-destructive text-sm">{error}</p> : null}
          <Button type="submit" className="w-full" disabled={busy}>
            {busy ? "Creating…" : "Create account"}
          </Button>
        </form>
      </div>
    </div>
  )
}
