/** Resolved at build time. Set `VITE_LOGIN_URL` to the full dashboard sign-in URL when not using the default host. */
function resolveLoginHref(): string {
  const raw = import.meta.env.VITE_LOGIN_URL
  if (typeof raw === "string") {
    const t = raw.trim().replace(/\/$/, "")
    if (t !== "") {
      return t
    }
  }
  if (import.meta.env.DEV) {
    return "http://localhost:5173/login"
  }
  return "https://app.kindling.systems/login"
}

export const loginHref = resolveLoginHref()
