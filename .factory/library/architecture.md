# Architecture

How the Kindling system works at a high level.

## Overview

Kindling is a self-hosted PaaS running on Cloud Hypervisor microVMs. A single Go binary (`cmd/kindling`) acts as control plane, build worker, and edge proxy. All state lives in PostgreSQL.

## Core Components

### State Machine: WAL → Reconciler
All state transitions are event-driven via PostgreSQL logical replication:
1. A database row changes (INSERT/UPDATE)
2. `internal/listener` picks up the change via pglogrepl WAL stream
3. It dispatches the entity ID to the appropriate reconciler
4. The reconciler (`internal/reconciler` generic scheduler) reads current state and converges toward desired state
5. Failed reconciliations retry after 5s

### Package Map
```
cmd/kindling/          — CLI + server startup (cobra)
internal/
  auth/                — Session + API key auth middleware
  bootstrap/           — Initial setup flow
  builder/             — OCI image builds (buildah, framework detection)
  chbridge/            — Cloud Hypervisor bridge proxy
  cli/                 — CLI HTTP client
  config/              — Cluster settings manager (PG NOTIFY hot reload)
  database/            — PG connection + sqlc generated queries
  deploy/              — Deployment reconciler (VM provisioning, health checks, scale-to-zero)
  edgeproxy/           — HTTP reverse proxy + CertMagic TLS
  githubapi/           — GitHub API client
  listener/            — PostgreSQL WAL listener
  migrationreconcile/  — Live migration reconciler
  oauth/               — OAuth/OIDC provider support
  oci/                 — OCI image helpers (skopeo, umoci)
  preview/             — PR preview environment cleanup
  reconciler/          — Generic reconciler scheduler
  rpc/                 — REST API layer with root router + domain subpackages (auth/projects/deployments/volumes/domains/servers/settings)
  runtime/             — VM/container lifecycle (Cloud Hypervisor, crun, Apple VZ)
  server/              — Server registration + heartbeat
  serverreconcile/     — Server drain/failover reconciler
  shared/pguuid/       — Shared UUID/pgtype conversion helpers
  shared/uuid/         — Shared UUID utilities
  usage/               — Resource usage polling
  vmm/                 — VM monitor (process lifecycle)
  volumebackup/        — Volume backup executor
  volumeops/           — Volume operation reconciler
  webhook/             — GitHub webhook handler
```

### Key Data Flow
Git push → GitHub webhook → deployment row INSERT → WAL listener → deployment reconciler → build → VM provision → edge proxy routes traffic

### Import Graph Rules
- Strictly layered: shared → database → domain packages → rpc → cmd
- No circular imports allowed
- `internal/shared/` is the leaf — imported by many, imports nothing internal
- `internal/rpc/` is the API layer — imports domain packages but never imported by them
- `cmd/kindling/` is the root — wires everything together

## Patterns

### Error Handling
- `fmt.Errorf("context: %w", err)` for wrapping
- `writeAPIError(w, status, code, msg)` for HTTP error responses
- Reconciler errors trigger retry after 5s

### Configuration
- `cluster_settings` table + PG NOTIFY for hot reload
- `cluster_secrets` encrypted with AES-GCM master key
- Server settings per-host in `server_settings` table

### Logging
- slog (stdlib) throughout, structured fields
- `cmd/guest-agent/` uses stdlib log (runs inside VMs, separate concern)
