# GitHub Actions — control plane deploy

Two workflows live under `.github/workflows/`:

- **`ci.yml`** — on every pull request and push to `main`: `go vet`, `go test ./...`, and a production-style dashboard build. Requires the repository secret **`VITE_API_URL`** (API base URL for the Vite build, e.g. `https://api.kindling.systems`).
- **`deploy-prod.yml`** — on push to `main` and manual **`workflow_dispatch`**: builds `linux/amd64` `bin/kindling`, builds the dashboard, optionally rebuilds the microVM **kernel** or **initramfs** when relevant paths change, then deploys over SSH.

## `production` environment

The deploy job uses `environment: production`. In the repo’s **Settings → Environments → production**, you can:

- Add **required reviewers** so deploys wait for approval.
- Scope **secrets and variables** to production only.

Until `production` exists, GitHub creates it on first run (with default repo rules).

| Name | Required | Description |
|------|----------|-------------|
| `SSH_PRIVATE_KEY` | Yes | Private key for the deploy user (PEM). Loaded via [webfactory/ssh-agent](https://github.com/webfactory/ssh-agent). |
| `SSH_HOST` | Yes | Hostname or IP of the Kindling server. |
| `SSH_USER` | Yes | SSH user (e.g. `ubuntu`). Must match `User=` in `kindling.service` / `KINDLING_HOME`. |
| `SSH_PORT` | No | SSH port; defaults to `22`. |
| `SSH_KNOWN_HOSTS` | No | One host key per line for `known_hosts`. If omitted, the workflow runs `ssh-keyscan` (TOFU). Prefer pinning keys in production. |

## Secrets (CI)

| Name | Required | Description |
|------|----------|-------------|
| `VITE_API_URL` | Yes (for `ci.yml` dashboard job) | API origin baked into the dashboard static build (e.g. `https://api.kindling.systems`). |

## Secrets (deploy)

The deploy user must be able to run **without a password**:

- `sudo setcap cap_net_admin,cap_net_bind_service+ep` on `$KINDLING_HOME/bin/kindling`
- `sudo systemctl restart kindling`

Configure `sudoers` accordingly (e.g. a drop-in for those commands only).

## Variables (optional)

Repository or **production** environment variables:

| Name | Default used in workflow | Description |
|------|-------------------------|-------------|
| `VITE_API_URL` | _none — CI fails if unset_ | **CI** (`ci.yml`): passed to Vite for the dashboard build. Set under **Settings → Secrets and variables → Actions → Variables**. |
| `DEPLOY_VITE_API_URL` | `https://api.kindling.systems` if unset | **Deploy** (`deploy-prod.yml`): production dashboard API origin. |
| `KINDLING_HOME` | `/home/$SSH_USER/kindling` | Deploy path for `bin/kindling` and `web/dashboard/`. |
| `KINDLING_DATA` | `/home/$SSH_USER/.kindling` | Where `vmlinuz.bin` and `initramfs.cpio.gz` are copied when those artifacts are built. |

## Conditional kernel and initramfs

- **Kernel** runs when `scripts/build-kernel.sh` changes, or when **`workflow_dispatch`** sets **force kernel**.
- **Initramfs** runs when `cmd/guest-agent/`, `scripts/build-initramfs-local.sh`, or `scripts/build-initramfs.sh` change, or **force initramfs** is set.

Kernel builds are slow; a persistent tree under `KINDLING_KERNEL_WORKDIR` is cached between runs (see `scripts/build-kernel.sh`).

## Server prerequisites

One-time: [setup-kindling-prod.sh](setup-kindling-prod.sh), edited `/etc/kindling/kindling.env`, and `systemctl enable --now kindling`. The workflow only updates binaries, static dashboard files, and optionally kernel/initramfs artifacts — it does **not** run database migrations.

## Manual deploy options

**`workflow_dispatch`** on `Deploy production` allows forcing a kernel or initramfs rebuild without touching those paths.
