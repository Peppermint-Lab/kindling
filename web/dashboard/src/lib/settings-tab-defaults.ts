export function resolveOrgSettingsDefaultTab(opts: {
  tab: string | null
  authError: string | null
  authLinked: string | null
  canManageOrg: boolean
}): string {
  if (opts.authError || opts.authLinked) return "authentication"
  const t = opts.tab
  if (t === "providers") return "providers"
  if (t === "cluster" && opts.canManageOrg) return "cluster"
  if (t === "public-url" || t === "authentication") return "authentication"
  if (opts.canManageOrg) return "cluster"
  return "authentication"
}

export function resolvePlatformSettingsDefaultTab(tab: string | null): "public-url" | "sign-in-providers" | "health" {
  if (tab === "sign-in-providers") return "sign-in-providers"
  if (tab === "health") return "health"
  return "public-url"
}
