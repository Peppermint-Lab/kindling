# Remote VM operations guide (Milestone 6)

Engineer- and operator-oriented notes for Kindling **remote VMs** on the **server** product. For runtime selection and capability keys, see [remote-vm-runtime-capabilities.md](remote-vm-runtime-capabilities.md).

## Access surfaces (three entry points)

| Surface | What the user does | Implementation notes |
|--------|---------------------|----------------------|
| **Browser app** | Open the published HTTP URL (dashboard or `runtime_url`) | Published port / edge hostname; not the same as SSH. |
| **Dashboard shell** | Terminal card on the VM detail page | WebSocket `GET /api/vms/{id}/shell/ws`, PTY via shellwire; cookie-authenticated like the rest of the dashboard. |
| **SSH (CLI)** | `kindling vm ssh --vm <uuid>` on their machine | Local `ssh` + `ProxyCommand`; tunnels via `GET /api/vms/{id}/ssh/ws`. **No** in-browser SSH client. |

Browser-native SSH (xterm + SSH protocol in the SPA) remains **out of scope** for this track.

## HTTP API (VM CRUD and access)

Routes use the **`/api/vms`** prefix. Common calls:

- `GET /api/vms` — list (includes `capabilities` when built by the server).
- `GET /api/vms/{id}` — detail.
- `POST /api/vms` / `PATCH /api/vms/{id}` — create/update (org admin).
- Lifecycle: `POST /api/vms/{id}/{start|stop|suspend|resume}`.
- **Exec / copy:** `POST /api/vms/{id}/exec`, `copy-in`, `GET copy-out`.
- **Observability (guest):** `GET /api/vms/{id}/logs`, `GET /api/vms/{id}/stats` — meaningful when `observed_state` is **`running`** and the worker that owns the VM can serve them (see proxy note below).
- **Access audit:** access events are recorded for shell, SSH, exec, copy (see SQL / RPC around remote VM access events).

**Error responses:** The JSON `code` field may still use historical identifiers (e.g. `sandbox_runtime`, `sandbox_shell`). Human-readable `error` strings use **remote VM** wording where the API returns a fixed message.

## Logs, stats, and deployment usage

- **Per-VM guest logs/stats** (`/api/vms/{id}/logs`, `/api/vms/{id}/stats`): Pulled from the guest path the runtime exposes (e.g. guest-agent `GET /stats` over vsock for microVMs; `crun` uses host-visible process metrics). Empty or null responses are normal when the VM is not running or the owning worker cannot collect yet.
- **Deployment instance usage samples** (`internal/usage`): The worker **resource poller** samples `ResourceStats` for **deployment instances** and writes `instance_usage_samples`. That path is separate from remote VM guest stats but shares the same style of cumulative CPU / RSS / disk counters where the runtime implements them.

## Worker placement and heartbeat

Workers send heartbeat metadata used for scheduling (see [remote-vm-runtime-capabilities.md](remote-vm-runtime-capabilities.md)):

- `remote_vm_backend`, `remote_vm_arch`, placement eligibility flags, `remote_vm_capabilities`, `remote_vm_enabled`.

**Isolation policy** on the row (`best_available` vs `require_microvm`) expresses intent; **`backend`** and **`arch`** reflect what actually ran.

## Linux runtime selection (operator mental model)

On Linux workers:

- **Cloud Hypervisor** when KVM is available and `cloud-hypervisor` is on `PATH`.
- Otherwise **`crun`** fallback when policy allows **`best_available`**.

Authoritative detection: `internal/runtime.DescribeHostRuntime` — do not duplicate probes elsewhere.

## Troubleshooting

| Symptom | Things to check |
|---------|------------------|
| SSH “not ready” in UI | `ssh_host_public_key` on the VM row; guest must expose sshd; `syncSandboxSSHAccess` path on worker. |
| Shell disconnects or 409 | VM `observed_state`, worker logs; codes like `sandbox_shell` on errors. |
| Stats/logs empty | VM must be **running**; request may be proxied to the owning worker (`sandbox_proxy` / internal API forward). |
| Wrong backend vs expectation | Worker KVM + `cloud-hypervisor`; `isolation_policy`; template-pinned backend vs live worker metadata. |

For internal DNS behavior of hosted workloads (separate from remote VM access), see [internal-dns-runtime.md](internal-dns-runtime.md). For control-plane HA, see [high-availability.md](high-availability.md).

## Internal naming: why `sandbox` still appears in code

The **product** and **HTTP contract** use **VM** terms (`/api/vms`, `remote_vms`, CLI `kindling vm`). Some **implementation** packages and symbols retain `sandbox` from before the rename, for example:

- `internal/sandbox` — reconciler and placement service for remote VMs (internal-only; not a user-facing API surface).
- `internal/rpc/sandboxes.go` — HTTP handlers for `/api/vms` (file name unchanged; handlers are VM routes).
- JSON **error `code`** values such as `sandbox_exec` — stable for clients; prefer documenting codes in this file rather than renaming without a compatibility plan.

New user-facing strings (CLI help, API `error` text, dashboard copy) should say **remote VM** unless referring to an intentional internal identifier.
