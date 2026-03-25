import type { Deployment } from "@/lib/api"

export function phaseLabel(phase: string): string {
  const map: Record<string, string> = {
    pending: "Pending",
    queued: "Queued",
    building: "Building",
    starting: "Starting",
    running: "Running",
    failed: "Failed",
    stopped: "Stopped",
  }
  return map[phase] ?? phase
}

export function phaseVariant(
  phase: string,
): "default" | "secondary" | "destructive" | "outline" {
  if (phase === "failed") return "destructive"
  if (phase === "running") return "default"
  if (phase === "stopped") return "secondary"
  if (phase === "building" || phase === "starting" || phase === "queued") return "outline"
  return "outline"
}

export function isTerminalDeployment(dep: Deployment): boolean {
  return ["running", "failed", "stopped"].includes(dep.phase)
}
