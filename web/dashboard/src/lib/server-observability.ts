import type { Server } from "@/lib/api"
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

export function formatPercent(value?: number, digits = 0): string {
  if (value == null || !Number.isFinite(value)) return "—"
  return `${value.toFixed(digits)}%`
}

export function formatRPS(value?: number): string {
  if (value == null || !Number.isFinite(value)) return "0.0"
  if (value >= 100) return value.toFixed(0)
  if (value >= 10) return value.toFixed(1)
  return value.toFixed(2)
}

export function ratioPercent(used?: number, total?: number): number {
  if (used == null || total == null || !Number.isFinite(used) || !Number.isFinite(total) || total <= 0) return 0
  return Math.max(0, Math.min(100, (used / total) * 100))
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

function hostIsUnhealthy(server: Server): boolean {
  const sampleHealth = server.host_metrics?.sample_health
  return server.health !== "healthy" || sampleHealth === "stale" || sampleHealth === "missing"
}

export function sortControlPlaneHosts(hosts: Server[]): Server[] {
  return [...hosts].sort((a, b) => {
    const unhealthyDiff = Number(hostIsUnhealthy(b)) - Number(hostIsUnhealthy(a))
    if (unhealthyDiff !== 0) return unhealthyDiff

    const cpuDiff = (b.host_metrics?.cpu_percent ?? 0) - (a.host_metrics?.cpu_percent ?? 0)
    if (cpuDiff !== 0) return cpuDiff

    const memoryDiff = (b.host_metrics?.memory_used_bytes ?? 0) - (a.host_metrics?.memory_used_bytes ?? 0)
    if (memoryDiff !== 0) return memoryDiff

    return (a.hostname || a.id).localeCompare(b.hostname || b.id)
  })
}
