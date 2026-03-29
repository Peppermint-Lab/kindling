# GitHub Actions — control plane deploy

Two workflows live under `.github/workflows/`:

- **`ci.yml`** — on every pull request and push to `main`: `go vet`, `go test ./...`, and a production-style dashboard build. The `go` job now targets a Kindling self-hosted runner with `runs-on: [self-hosted, kindling, linux, x64]`; the `dashboard` job stays on `ubuntu-latest` during the rollout. Requires the repository secret **`VITE_API_URL`** (API base URL for the Vite build, e.g. `https://api.kindling.systems`).
- **`deploy-prod.yml`** — on push to `main` and manual **`workflow_dispatch`**: builds `linux/amd64` `bin/kindling`, builds the dashboard, optionally rebuilds the microVM **kernel** or **initramfs** when relevant paths change, then deploys over SSH.

## Kindling GitHub runners

Kindling’s GitHub-native runner support uses the same pattern as Depot or Actuated:

- GitHub remains the workflow scheduler and source of step logs / artifacts.
- Kindling listens for `workflow_job` webhooks.
- For jobs labeled `self-hosted` and `kindling`, Kindling provisions a fresh microVM runner, registers it with GitHub using a just-in-time runner config, runs exactly one job, and tears it down.

### Rollout in this repo

This repo now moves only the `go` job in `.github/workflows/ci.yml` onto Kindling runners:

```yaml
runs-on: [self-hosted, kindling, linux, x64]
```

Leave `dashboard` and `deploy-prod.yml` on GitHub-hosted runners until the Kindling runner path is stable in production.

### GitHub App setup

Create a GitHub App for the org that owns this repo and:

- install it on the org / repositories you want Kindling to serve
- subscribe it to the **`workflow_job`** webhook event
- grant org-level self-hosted runner write permissions
- point the webhook URL at `https://<kindling-api>/webhooks/github`

The webhook secret configured in the GitHub App must match the Kindling provider connection credentials.

### Kindling provider connection

Create an org provider connection with `provider: "github"` and GitHub Actions runner metadata.

Example payload for `POST /api/org/provider-connections`:

```json
{
  "provider": "github",
  "external_slug": "kindlingvm",
  "display_label": "GitHub Actions runners",
  "token": "{\"app_private_key_pem\":\"-----BEGIN RSA PRIVATE KEY-----\\n...\\n-----END RSA PRIVATE KEY-----\",\"webhook_secret\":\"replace-me\"}",
  "metadata": {
    "mode": "actions_runner",
    "org_login": "kindlingvm",
    "app_id": 123456,
    "installation_id": 7890123,
    "runner_group_id": 1,
    "default_labels": ["self-hosted", "kindling", "linux", "x64"]
  }
}
```

Metadata fields used by Kindling:

- `mode`: must be `actions_runner`
- `org_login`: GitHub organization login
- `app_id`: GitHub App ID
- `installation_id`: installation ID for the org
- `runner_group_id`: GitHub runner group to register runners into
- `default_labels`: labels Kindling should attach to JIT runners

Credential fields stored in `token`:

- `app_private_key_pem`
- `webhook_secret`

### Worker prerequisites

The Kindling worker serving GitHub Actions runners must already have the microVM prerequisites used by Kindling CI:

- Linux: `cloud-hypervisor`, KVM, kernel, initramfs, and builder rootfs
- macOS: Apple Virtualization runner assets under `~/.kindling/`

If those are missing, Kindling will accept the webhook but fail the GitHub-backed CI job during provisioning.

## `production` environment

The deploy job uses `environment: production`. In the repo’s **Settings → Environments → production**, you can:

- Add **required reviewers** so deploys wait for approval.
- Scope **secrets and variables** to production only.

Until `production` exists, GitHub creates it on first run (with default repo rules).

## Secrets (CI)

| Name | Required | Description |
|------|----------|-------------|
| `VITE_API_URL` | Yes (for `ci.yml` dashboard job) | API origin baked into the dashboard static build (e.g. `https://api.kindling.systems`). |

## Secrets (deploy)

| Name | Required | Description |
|------|----------|-------------|
| `SSH_PRIVATE_KEY` | Yes | Private key for the deploy user (PEM). Loaded via [webfactory/ssh-agent](https://github.com/webfactory/ssh-agent). |
| `SSH_HOST` | Yes | Hostname or IP of the Kindling server. |
| `SSH_USER` | Yes | SSH user (e.g. `ubuntu`). Must match `User=` in `kindling.service` / `KINDLING_HOME`. |
| `SSH_PORT` | No | SSH port; defaults to `22`. |
| `SSH_KNOWN_HOSTS` | No | One host key per line for `known_hosts`. If omitted, the workflow runs `ssh-keyscan` (TOFU). Prefer pinning keys in production. |

The deploy user must be able to run **without a password**:

- `sudo setcap cap_net_admin,cap_net_bind_service+ep` on `$KINDLING_HOME/bin/kindling`
- `sudo systemctl restart kindling`

Configure `sudoers` accordingly (e.g. a drop-in for those commands only).

## Variables (optional)

Repository or **production** environment variables:

| Name | Default used in workflow | Description |
|------|-------------------------|-------------|
| `DEPLOY_VITE_API_URL` | `https://api.kindling.systems` if unset | **Deploy** (`deploy-prod.yml`): production dashboard API origin (repository or environment **variable**). |
| `KINDLING_HOME` | `/home/$SSH_USER/kindling` | Deploy path for `bin/kindling` and `web/dashboard/`. |
| `KINDLING_DATA` | `/home/$SSH_USER/.kindling` | Where `vmlinuz.bin` and `initramfs.cpio.gz` are copied when those artifacts are built. |

## Conditional kernel and initramfs

- **Kernel** runs when `scripts/build-kernel.sh` changes, or when **`workflow_dispatch`** sets **force kernel**.
- **Initramfs** runs when `cmd/guest-agent/`, `scripts/build-initramfs-local.sh`, or `scripts/build-initramfs.sh` change, or **force initramfs** is set.

Kernel builds are slow; a persistent tree under `KINDLING_KERNEL_WORKDIR` is cached between runs (see `scripts/build-kernel.sh`).

## Server prerequisites

One-time: [setup-kindling-prod.sh](setup-kindling-prod.sh), edited `/etc/kindling/kindling.env`, and `systemctl enable --now kindling`. The workflow only updates binaries, static dashboard files, and optionally kernel/initramfs artifacts — it does **not** run database migrations.

Production installs now also enable `kindling-networking.service`, which reapplies the host `iptables` and `net.ipv4.ip_forward=1` settings Kindling’s TAP-backed microVMs need after every boot. Override the detected interface or CIDRs in `/etc/kindling/kindling.env` with `KINDLING_VM_EGRESS_IFACE`, `KINDLING_VM_EGRESS_CIDRS`, or `KINDLING_VM_TAP_PREFIXES` if your host uses non-default routing.

## Manual deploy options

**`workflow_dispatch`** on `Deploy production` allows forcing a kernel or initramfs rebuild without touching those paths.
