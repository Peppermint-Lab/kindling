import { cn } from "@/lib/utils"

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let value = n
  let idx = 0
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024
    idx++
  }
  return `${value.toFixed(idx === 0 ? 0 : 1)} ${units[idx]}`
}

export function formatAgeSeconds(seconds?: number): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return "—"
  if (seconds < 60) return `${Math.floor(seconds)}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

export function healthBadgeClass(health?: string): string {
  switch (health) {
    case "healthy":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
    case "degraded":
      return "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300"
    case "stale":
      return "border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-300"
    default:
      return "border-border bg-muted text-muted-foreground"
  }
}

export function healthChipClass(health?: string, extra?: string): string {
  return cn("border", healthBadgeClass(health), extra)
}

export function componentLabel(name: string): string {
  switch (name) {
    case "usage_poller":
      return "Poller"
    case "api":
      return "API"
    case "edge":
      return "Edge"
    case "worker":
      return "Worker"
    default:
      return name
  }
}
