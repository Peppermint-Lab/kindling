import type { Deployment } from "@/lib/api"

export function phaseLabel(phase: string): string {
  const map: Record<string, string> = {
    pending: "Queued",
    queued: "Queued",
    building: "Building",
    starting: "Building",
    running: "Ready",
    failed: "Error",
    stopped: "Canceled",
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

export type PhaseDotColor = "green" | "amber" | "red" | "gray"

export function phaseDotColor(phase: string): PhaseDotColor {
  if (phase === "running") return "green"
  if (phase === "building" || phase === "starting") return "amber"
  if (phase === "failed") return "red"
  return "gray"
}

export type PhaseFilterGroup = "ready" | "building" | "error" | "canceled"

export function phaseToFilterGroup(phase: string): PhaseFilterGroup {
  if (phase === "running") return "ready"
  if (phase === "building" || phase === "starting" || phase === "queued" || phase === "pending") return "building"
  if (phase === "failed") return "error"
  return "canceled"
}

export function isTerminalDeployment(dep: Deployment): boolean {
  return ["running", "failed", "stopped"].includes(dep.phase)
}
