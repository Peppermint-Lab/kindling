import type { Sandbox } from "@/lib/api"

function normalizeDirectBrowserUrl(raw: string): string {
  const direct = raw.trim()
  if (!direct) return ""
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(direct)) {
    return direct
  }
  return `http://${direct}`
}

export function publicBrowserAppUrl(sandbox: Sandbox): string | null {
  const direct = sandbox.runtime_url?.trim()
  if (direct) return normalizeDirectBrowserUrl(direct)
  const host = sandbox.published_ports?.find((p) => p.public_hostname?.trim())?.public_hostname?.trim()
  if (host) {
    return `https://${host}`
  }
  return null
}

export function remoteVmSshCliCommand(vmId: string): string {
  return `kindling vm ssh --vm ${vmId}`
}

export type AccessSurfaceStatus = "ready" | "blocked" | "unsupported"

export function browserAppAccessStatus(sandbox: Sandbox): {
  status: AccessSurfaceStatus
  hint: string
} {
  const cap = sandbox.capabilities?.browser_app
  if (cap && !cap.supported) {
    return { status: "unsupported", hint: "This backend does not publish a browser app URL for remote VMs." }
  }
  if (sandbox.observed_state !== "running") {
    return { status: "blocked", hint: "Start the remote VM to open or copy the app URL." }
  }
  if (!publicBrowserAppUrl(sandbox)) {
    return {
      status: "blocked",
      hint: "Publish an HTTP port (see HTTP Publish below) to get a stable URL, then it will appear here.",
    }
  }
  if (cap && !cap.available) {
    return { status: "blocked", hint: "Browser app access will be available once the VM is fully running." }
  }
  return { status: "ready", hint: "Open your app in the browser or copy the URL to share." }
}

export function sshAccessStatus(sandbox: Sandbox): {
  status: AccessSurfaceStatus
  hint: string
} {
  const cap = sandbox.capabilities?.ssh_tcp
  if (cap && !cap.supported) {
    return { status: "unsupported", hint: "SSH via the Kindling CLI is not supported on this backend." }
  }
  if (sandbox.observed_state !== "running") {
    return { status: "blocked", hint: "Start the remote VM, then connect with the CLI using the command below." }
  }
  const key = sandbox.ssh_host_public_key?.trim()
  if (!key) {
    return {
      status: "blocked",
      hint: "Waiting for SSH host key from the guest. Ensure the image runs sshd and ssh-keygen, then refresh.",
    }
  }
  if (cap && !cap.available) {
    return { status: "blocked", hint: "SSH will be available once the VM is fully running." }
  }
  return {
    status: "ready",
    hint: "Use your local ssh client; Kindling verifies the host key. Add your public key under Settings → SSH Keys.",
  }
}

export function terminalShellAccessStatus(sandbox: Sandbox): {
  status: AccessSurfaceStatus
  hint: string
} {
  const cap = sandbox.capabilities?.terminal_shell
  if (cap && !cap.supported) {
    return { status: "unsupported", hint: "In-browser shell is not supported on this worker backend." }
  }
  if (sandbox.observed_state !== "running") {
    return { status: "blocked", hint: "Start the remote VM, then connect to open a guest shell." }
  }
  if (cap && !cap.available) {
    return { status: "blocked", hint: "Shell access becomes available once the VM is fully running." }
  }
  return { status: "ready", hint: "Guest PTY over the shell WebSocket." }
}

export function humanizeShellConnectionError(raw: string): string {
  const m = raw.trim().toLowerCase()
  if (!m) return "Could not connect to the shell. Check that the VM is running and try again."
  if (
    m.includes("not running") ||
    m.includes("stopped") ||
    m.includes("sandbox not running") ||
    m.includes("remote vm must be running")
  ) {
    return "The remote VM is not running. Start it from the toolbar above, then connect again."
  }
  if (m.includes("sandbox_shell") || m.includes("conflict")) {
    return "The worker could not start a shell right now. Wait a few seconds, restart the VM, or try again."
  }
  if (m.includes("403") || m.includes("forbidden") || m.includes("unauthorized")) {
    return "Access was denied. Refresh the page if your session expired, or confirm you can open this VM."
  }
  if (m.includes("404") || m.includes("not found")) {
    return "This VM was not found or is no longer available."
  }
  return raw
}
