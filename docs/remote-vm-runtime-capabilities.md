# Remote VM runtime detection and capabilities (Milestone 2)

This document describes the **in-repo** contract for Kindling **remote VMs** after Milestone 2 of the “Remote VM Experience” track.

Canonical product notes may still live in the Obsidian vault; this file is the engineer-oriented summary tied to the code.

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

`crun` honestly reports **no** guest shell/SSH/exec support until Milestone 4 parity work lands; it still reports **browser_app**, **suspend_resume**, and **template_clone** where the runtime supports those lifecycles.

## Worker heartbeat metadata

Workers emit (among other keys):

- `remote_vm_backend`, `remote_vm_arch`
- `remote_vm_linux_placement_eligible` / `remote_vm_mac_placement_eligible`
- `remote_vm_capabilities`: map of capability name → `true`/`false` (**supported** on this worker)
- `remote_vm_enabled`: `true` when either placement eligibility flag is true (schedulable for remote VM workloads under current rules)

**Milestone 2 vs 3 boundary:** Linux **`crun`** workers may report capabilities for operators, but **`remote_vm_linux_placement_eligible` stays false** until Milestone 3 allows placement onto `crun`.

Placement selection in `internal/sandbox.Service.pickServer` uses explicit eligibility JSON when present, and falls back to legacy behavior (`remote_vm_backend == cloud-hypervisor` / `apple-vz`) when older workers have not upgraded.

## API and consumers

- VM and template JSON include optional `capabilities` objects built server-side.
- Dashboard and `kindling vm list` surface backend and capability summaries without inferring from ad hoc strings.
