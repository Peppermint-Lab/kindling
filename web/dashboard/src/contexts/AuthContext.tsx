import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react"
import { api, type AuthSession } from "@/lib/api"

type AuthContextValue = {
  session: AuthSession | null
  loading: boolean
  refresh: () => Promise<void>
  logout: () => Promise<void>
  switchOrg: (organizationId: string) => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<AuthSession | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    const s = await api.authSession()
    setSession(s)
  }, [])

  const logout = useCallback(async () => {
    await api.authLogout()
    setSession({ authenticated: false })
  }, [])

  const switchOrg = useCallback(async (organizationId: string) => {
    const s = await api.authSwitchOrg(organizationId)
    setSession(s)
  }, [])

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const s = await api.authSession()
        if (!cancelled) setSession(s)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const value = useMemo(
    () => ({ session, loading, refresh, logout, switchOrg }),
    [session, loading, refresh, logout, switchOrg],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth outside AuthProvider")
  return ctx
}
