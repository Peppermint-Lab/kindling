# Remote VM runtime detection and capabilities (through Milestone 5)

This document describes the **in-repo** contract for Kindling **remote VMs** after Milestone 4 of the “Remote VM Experience” track (M1–M3 renames, detection, and Linux `crun` placement, plus **`crun` access parity**: isolated network per container, app port forwarding, exec/copy, shell, and SSH over the same RPC contracts as Cloud Hypervisor). **Milestone 5** adds dashboard and CLI UX for those access surfaces.

Canonical product notes may live in the Obsidian vault; this file is the engineer-oriented summary tied to the code.

## Authoritative runtime selection

- Use `internal/runtime.DescribeHostRuntime` with a populated `HostRuntimeConfig` (same inputs as `NewDetectedRuntime`).
- Linux: **Cloud Hypervisor** only when `/dev/kvm` is present **and** `cloud-hypervisor` is on `PATH`; otherwise **`crun`**.
- Linux overrides: `ForceRuntime` of `crun` / `docker` / `oci` forces **`crun`**; `cloud-hypervisor` or `ch` forces **Cloud Hypervisor**.
- macOS: **Apple Virtualization** (`apple-vz`).
- `Detect()` in `internal/runtime` is a thin wrapper over `DescribeHostRuntime(HostRuntimeConfig{})` (empty overrides).

Do not duplicate KVM / `cloud-hypervisor` probing outside `internal/runtime`.

## Product capability model

Stable JSON keys on each remote VM (and template) summary:

| Key | Meaning |
|-----|---------|
| `browser_app` | Reachable app HTTP surface / published runtime URL |
| `terminal_shell` | Browser or API shell session into the guest |
| `ssh_tcp` | SSH (via Kindling-managed proxy path) |
| `exec_copy` | Exec and copy-in/out |
| `suspend_resume` | Suspend/resume lifecycle |
| `template_clone` | Warm / template clone |
| `live_migration` | Live migration (Cloud Hypervisor only today) |

Each entry is `{ "supported": bool, "available": bool }`:

- **supported**: backend can provide this in principle.
- **available**: usable **now** given `observed_state` (typically requires `running`).

On **Linux `crun`**, workloads run in **isolated network + PID namespaces** rather than host networking. The worker creates a per-instance veth, reuses Kindling's existing host forwarding/NAT setup for remote VM egress, and publishes the app on a loopback port via a host-to-guest forward. Shell, SSH, exec, and copy use the same `GuestAccess` / `GuestStreamAccess` / `GuestTCPAccess` interfaces as Cloud Hypervisor, backed by `crun exec`, PTY + shellwire bridging for the browser terminal, and TCP dials into the container netns. **Live migration** remains unsupported on `crun`.

## Worker heartbeat metadata

Workers emit (among other keys):

- `remote_vm_backend`, `remote_vm_arch`
- `remote_vm_linux_placement_eligible` / `remote_vm_mac_placement_eligible`
- `remote_vm_capabilities`: map of capability name → `true`/`false` (**supported** on this worker)
- `remote_vm_enabled`: `true` when either placement eligibility flag is true (schedulable for remote VM workloads under current rules)

**Milestone 3 placement:** Linux **`crun`** workers set **`remote_vm_linux_placement_eligible: true`** in heartbeat metadata so the scheduler can place `linux-remote-vm` workloads onto them when `isolation_policy` is **`best_available`** and no Cloud Hypervisor worker is available. With **`require_microvm`**, Linux placement only considers **`cloud-hypervisor`** workers.

Remote VM rows store **`isolation_policy`** (requested intent) separately from **`backend`** / **`arch`** (resolved placement). See `internal/sandbox.Service.assignSandbox` / `pickServer`.

Placement selection in `internal/sandbox.Service.pickServer` uses explicit eligibility JSON when present, and falls back to legacy behavior (`remote_vm_backend == cloud-hypervisor` / `apple-vz`) when older workers have not upgraded. For **`best_available`** on Linux with no pinned backend, eligible workers are ordered so **`cloud-hypervisor` precedes `crun`**.

## API and consumers

- VM and template JSON include optional `capabilities` objects built server-side.
- Dashboard and `kindling vm list` surface backend and capability summaries without inferring from ad hoc strings.

## Milestone 5: browser, terminal, and SSH UX (product surfaces)

Milestone 5 does **not** change the access contracts above; it makes them **discoverable and resilient** in the UI and CLI help text.

- **Dashboard — Connect:** Remote VM detail shows **Browser app** and **SSH (CLI)** as peer entry surfaces (`Open app`, `Copy URL`, `Copy command`, host-key fingerprint, link to SSH keys). Helpers live in `web/dashboard/src/lib/remote-vm-access.ts`.
- **Dashboard — list:** Remote VMs list includes **Open app**, **Copy URL**, **Copy SSH**, and **Details** so users need not open the detail page first.
- **Dashboard — Shell card:** In-browser shell uses `/api/vms/{id}/shell/ws` with capability-aware disabled states, clearer errors, disconnect/reconnect, and stdin disabled when disconnected (`web/dashboard/src/components/sandbox-terminal.tsx`).
- **CLI:** `kindling vm ssh` remains the supported path for real SSH (local `ssh` + `ProxyCommand` + pinned known_hosts line). Prefer the dashboard **Connect** card or `kindling vm ssh --vm <uuid>` for copy-paste workflows.

### Explicitly out of scope: browser-native SSH

**In-browser SSH** (running OpenSSH or a full terminal SSH client inside the web app) is **not** part of Milestone 5. The product uses:

1. **Browser:** published HTTP app URL and in-dashboard **Shell** (WebSocket shellwire), not SSH-over-WebSockets in the browser UI.
2. **SSH:** the user’s **local** `ssh` binary via `kindling vm ssh`, which tunnels through `GET /api/vms/{id}/ssh/ws` under the hood.

If browser-native SSH becomes a requirement later, expect new UI (e.g. xterm.js + SSH protocol client or wasm), security review, and possibly different auth/session boundaries than the current cookie-authenticated dashboard shell.
