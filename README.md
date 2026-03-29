# Kindling

Self-hosted PaaS on microVMs. Push code, get production.

Kindling is an open-source, self-hosted platform-as-a-service that aims for Railway-like developer experience with Coolify-style ownership of your own infrastructure. On Linux hosts with KVM, deployments run in Cloud Hypervisor microVMs. On Linux hosts without KVM, Kindling falls back to `crun` OCI containers. On macOS development machines, it uses Apple Virtualization to run the same deploy pipeline locally.

## Why Kindling

- Git push and webhook-driven deploy flow for production and preview environments
- Declarative reconcilers triggered by PostgreSQL WAL changes instead of imperative pipelines
- Automatic TLS with CertMagic and on-demand Let's Encrypt certificates
- Framework detection for common app types when you do not provide a Dockerfile
- Single Go binary with PostgreSQL handling state, coordination, and leader election
- Best-available runtime detection across Cloud Hypervisor, Apple Virtualization, and `crun`

## Current Status

Kindling is pre-1.0, but the core deploy pipeline is working: source to build to runtime to live URL. Custom domains, encrypted project secrets, scaling primitives, and the dashboard/API are implemented. Multi-server networking and deeper operational hardening are still in progress.

## Architecture In One Minute

Kindling runs as a control plane backed by PostgreSQL.

- The API, dashboard backend, and CLI all talk to the same control plane.
- PostgreSQL stores state and also coordinates leader election with advisory locks.
- A WAL listener watches for row changes and schedules reconcilers in near real time.
- Reconcilers converge deployments, builds, instances, domains, and servers toward desired state.
- The edge proxy handles hostname routing and CertMagic-managed TLS.
- Worker/server nodes run workloads locally and participate in cluster reconciliation.
- Runtime selection is automatic per host: Cloud Hypervisor on Linux with KVM, Apple Virtualization on macOS development machines, and `crun` elsewhere.

## Quickstart: Single-Node Linux Host

For the shortest supported path on a real server:

```bash
make install-deps
make db
make build
bin/kindling serve
```

Notes:

- For a fuller host setup on Linux, you can run `sudo ./contrib/install-host-deps.sh --all`.
- Kindling looks for the Postgres DSN in `/etc/kindling/postgres.dsn` first, then `~/.kindling/postgres.dsn`, and otherwise falls back to `postgres://kindling:kindling@127.0.0.1:5432/kindling?sslmode=disable`.
- On first boot, `bin/kindling serve --public-url https://your-hostname` can seed the public API URL, and `bin/kindling serve --advertise-host 203.0.113.10` can seed the host/IP used for browser-openable runtime URLs.

See the Obsidian vault at `~/Desktop/vault/Kindling/` for all documentation. Key starting points:

- [[Kindling/notes/Getting Started]] — new host setup and operations
- [[Kindling/notes/Development]] — contributor workflow
- [[Kindling/notes/Architecture]] — control-plane model, reconcilers, runtimes
- [[Kindling/Spec]] — product spec and roadmap

The `docs/` directory in this repo contains only thin pointers to the vault.

## CLI Overview

The `kindling` CLI currently exposes these main command groups:

- `serve` to start the Kindling server
- `project` to create, list, inspect, and delete projects through the API
- `deploy` to create, inspect, or cancel deployments
- `domain` to manage custom domains
- `logs` to fetch build logs for a deployment
- `auth`, `context`, `status`, and `link` for remote API access and local CLI state
- `ci` for repository workflow execution
- `admin` for break-glass and host-local operations

Run `go run ./cmd/kindling --help` or `bin/kindling --help` for the current command surface.

## Development

See [[Kindling/notes/Development]] for the full contributor guide. The short version:

- `make build` builds the main binary
- `make dev` builds and starts the local server path
- `make dashboard-build` builds the embedded dashboard assets
- `make dev-up` and `make dev-down` manage the remote development loop
- Kernel and initramfs helpers exist for VM-backed local development, especially on macOS

## Docs

All canonical documentation lives in the Obsidian vault at `~/Desktop/vault/Kindling/`:

- [[Kindling/notes/Getting Started]] — new host setup
- [[Kindling/notes/Development]] — contributor workflow
- [[Kindling/notes/Architecture]] — control-plane model and runtimes
- [[Kindling/Spec]] — product spec and roadmap
- [[Kindling/notes/Operations]] — production deployment and day-two operations

The `docs/` directory in this repo contains thin pointers only.

## Project Layout

```text
cmd/
  guest-agent/      Guest init process used inside Kindling-managed VMs
  kindling/         Main binary (server + control plane + CLI)
internal/
  auth/             Dashboard/API authentication and session handling
  builder/          Build orchestration and framework detection
  database/         PostgreSQL access, schema, and sqlc queries
  deploy/           Deployment convergence and scaling logic
  edgeproxy/        HTTP/TLS edge proxy and routing
  listener/         PostgreSQL WAL listener
  preview/          Preview environment lifecycle
  reconciler/       Generic reconciler scheduler
  rpc/              API handlers used by the dashboard and control plane
  runtime/          Runtime abstraction and host-specific behavior
  usage/            Usage metering and rollups
  vmm/              VM management helpers
web/
  dashboard/        React + Vite dashboard SPA
  landing/          Static marketing site
docs/               Focused project documentation
contrib/            Install scripts, remote provisioning, and ops helpers
scripts/            Build and environment scripts
```

Further docs cleanup, including secondary README files, is still ahead. This first pass keeps the root README as the main public entrypoint and links deeper material out to focused docs.
