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

export function resolvePlatformSettingsDefaultTab(tab: string | null): "public-url" | "sign-in-providers" {
  return tab === "sign-in-providers" ? "sign-in-providers" : "public-url"
}
