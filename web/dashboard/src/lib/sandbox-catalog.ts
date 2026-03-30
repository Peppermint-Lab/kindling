export type SandboxImagePreset = {
  id: string
  name: string
  description: string
  host_group: "linux-remote-vm" | "mac-remote-vm"
  base_image_ref: string
  vcpu: number
  memory_mb: number
  disk_gb: number
  ssh_readiness: "ready" | "setup-required"
  note: string
}

export const sandboxImagePresets: SandboxImagePreset[] = [
  {
    id: "ubuntu-dev",
    name: "Ubuntu Dev Box",
    description: "General-purpose Linux sandbox with a familiar package ecosystem and the best odds of interactive success.",
    host_group: "linux-remote-vm",
    base_image_ref: "docker.io/library/ubuntu:24.04",
    vcpu: 2,
    memory_mb: 2048,
    disk_gb: 12,
    ssh_readiness: "ready",
    note: "Best default when you want shell access, apt, and room to install OpenSSH cleanly.",
  },
  {
    id: "debian-dev",
    name: "Debian Stable",
    description: "A leaner base than Ubuntu while still staying friendly to shell sessions and package installs.",
    host_group: "linux-remote-vm",
    base_image_ref: "docker.io/library/debian:12-slim",
    vcpu: 2,
    memory_mb: 2048,
    disk_gb: 10,
    ssh_readiness: "ready",
    note: "Good for durable team sandboxes that want predictable packages without a lot of extra image weight.",
  },
  {
    id: "node-workbench",
    name: "Node Workbench",
    description: "Preloaded Node runtime for quick app prototyping, package installs, and HTTP sandbox publishing.",
    host_group: "linux-remote-vm",
    base_image_ref: "docker.io/library/node:22-bookworm",
    vcpu: 2,
    memory_mb: 3072,
    disk_gb: 12,
    ssh_readiness: "ready",
    note: "A strong default for web backends, Vite apps, and small preview-style experiments.",
  },
  {
    id: "python-lab",
    name: "Python Lab",
    description: "Python runtime with a fuller Debian base so ad hoc tooling and SSH setup are less painful.",
    host_group: "linux-remote-vm",
    base_image_ref: "docker.io/library/python:3.12-bookworm",
    vcpu: 2,
    memory_mb: 3072,
    disk_gb: 12,
    ssh_readiness: "ready",
    note: "A safe choice for notebooks, automation, data scripts, and agent-style workloads.",
  },
  {
    id: "alpine-minimal",
    name: "Alpine Minimal",
    description: "Fast and tiny, but convenience features are on you. Keep it for tightly controlled, low-level setups.",
    host_group: "linux-remote-vm",
    base_image_ref: "docker.io/library/alpine:latest",
    vcpu: 1,
    memory_mb: 1024,
    disk_gb: 8,
    ssh_readiness: "setup-required",
    note: "Expect to install and start OpenSSH yourself before SSH access will work.",
  },
]

export function sandboxImageContract(baseImageRef: string) {
  const ref = baseImageRef.toLowerCase()
  const preset = sandboxImagePresets.find((candidate) => candidate.base_image_ref.toLowerCase() === ref)
  if (preset) {
    return {
      tone: preset.ssh_readiness === "ready" ? ("ready" as const) : ("caution" as const),
      title: preset.name,
      description: preset.note,
      recommended: preset.ssh_readiness === "ready",
    }
  }
  if (ref.includes("distroless") || ref.includes("scratch") || ref.includes("busybox")) {
    return {
      tone: "caution" as const,
      title: "Minimal image",
      description: "Shell, SSH, and guest tooling are unlikely to be present by default. Expect to add them yourself or switch to a fuller base image.",
      recommended: false,
    }
  }
  if (ref.includes("alpine")) {
    return {
      tone: "caution" as const,
      title: "Lightweight image",
      description: "Alpine works well, but SSH still depends on installing and starting OpenSSH inside the image.",
      recommended: false,
    }
  }
  return {
    tone: "ready" as const,
    title: "General-purpose image",
    description: "This is a better fit for interactive sandboxing, but Kindling still expects /bin/sh and benefits from sshd + ssh-keygen.",
    recommended: true,
  }
}

export function sandboxPresetByID(id: string) {
  return sandboxImagePresets.find((preset) => preset.id === id) ?? sandboxImagePresets[0]
}

export function hostGroupLabel(hostGroup: string) {
  if (hostGroup === "mac-remote-vm") return "macOS worker"
  return "Linux worker"
}

export function formatAutoSuspend(seconds: number) {
  if (seconds <= 0) return "Always on"
  if (seconds % 3600 === 0) return `Auto-suspends after ${seconds / 3600}h idle`
  if (seconds % 60 === 0) return `Auto-suspends after ${seconds / 60}m idle`
  return `Auto-suspends after ${seconds}s idle`
}

export function suggestedSandboxName(base: string) {
  const stamp = new Date().toISOString().slice(11, 16).replace(":", "")
  return `${base}-${stamp}`
}
