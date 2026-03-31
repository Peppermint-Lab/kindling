import { useEffect, useMemo, useState } from "react"
import { Link, useNavigate, useSearchParams } from "react-router-dom"
import { useAuth } from "@/contexts/AuthContext"
import { api, authProviderStartURL, type AuthPublicProvider } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function LoginPage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { refresh } = useAuth()
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [needsBootstrap, setNeedsBootstrap] = useState(false)
  const [providers, setProviders] = useState<AuthPublicProvider[]>([])

  const messageFromRedirect = useMemo(() => searchParams.get("auth_error"), [searchParams])

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const [bootstrap, authProviders] = await Promise.all([api.authBootstrapStatus(), api.authProviders()])
        if (!cancelled) {
          setNeedsBootstrap(bootstrap.needs_bootstrap)
          setProviders(authProviders)
        }
      } catch {
        if (!cancelled) {
          setNeedsBootstrap(false)
          setProviders([])
        }
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
        {providers.length > 0 ? (
          <div className="space-y-3">
            {providers.map((provider) => (
              <Button
                key={provider.provider}
                type="button"
                variant="outline"
                className="w-full"
                onClick={() => {
                  window.location.assign(authProviderStartURL(provider.provider, "login", "/"))
                }}
              >
                Continue with {provider.display_name}
              </Button>
            ))}
            <div className="relative py-1">
              <div className="absolute inset-0 flex items-center">
                <span className="w-full border-t" />
              </div>
              <div className="relative flex justify-center text-xs uppercase">
                <span className="bg-background px-2 text-muted-foreground">Or use email</span>
              </div>
            </div>
          </div>
        ) : null}
        <form
          className="space-y-4"
          onSubmit={async (e) => {
            e.preventDefault()
            setError(null)
            setBusy(true)
            try {
              const sess = await api.authLogin({ email, password })
              await refresh()
              navigate(sess.authenticated && sess.needs_onboarding ? "/onboarding" : "/", { replace: true })
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
          {error || messageFromRedirect ? <p className="text-destructive text-sm">{error || messageFromRedirect}</p> : null}
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
