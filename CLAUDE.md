# Kindling — Agent Guide

Self-hosted PaaS on Cloud Hypervisor microVMs. Push code, get production.

## Project Structure

```
cmd/
  kindling/          Main binary (control plane + server + CLI)
proto/               Protobuf definitions (Connect RPC)
gen/                 Generated Go code from protos
internal/
  database/          PostgreSQL connection + sqlc generated queries
  reconciler/        Generic reconciler scheduler
  listener/          PostgreSQL WAL listener (logical replication)
  builder/           Build orchestration (dind VM, framework detection)
  edgeproxy/         HTTP reverse proxy + CertMagic TLS
  zeitwerk/          Core service (VM lifecycle, deployment, server management)
  rpc/               RPC handlers (API layer)
  shared/            Shared utilities (crypto, uuid, etc.)
web/
  dashboard/         React + Vite + Tailwind + shadcn/ui SPA
```

## Build & Run

```bash
# Build
go build -o bin/kindling ./cmd/kindling

# Run (requires PostgreSQL)
bin/kindling serve --database-url postgres://localhost:5432/kindling

# CLI
bin/kindling project create --name myapp --repo github.com/user/myapp
bin/kindling deploy trigger --project myapp
```

## Tech Stack

- **Language:** Go 1.26
- **API:** Connect RPC (protobuf over HTTP)
- **Database:** PostgreSQL (sqlc for type-safe queries)
- **Hypervisor:** Cloud Hypervisor (vsock, qcow2)
- **TLS:** CertMagic (Let's Encrypt TLS-ALPN-01)
- **Build:** Docker buildx in isolated microVMs
- **CLI framework:** cobra
- **Logging:** slog (stdlib)
- **Telemetry:** OpenTelemetry

## Architecture

All state transitions are driven by **reconcilers**, not imperative pipelines:

1. A database row changes (INSERT/UPDATE)
2. The WAL listener picks up the change via PG logical replication
3. It schedules the entity's reconciler
4. The reconciler reads current state and converges toward desired state
5. Failed reconciliations retry after 5s

Leader election uses PG session-scoped advisory locks. The leader handles cluster-wide duties (builds, dead server detection, failover).

## Development Notes

- `go vet ./...` must pass
- Proto changes: edit `proto/` then `buf generate`
- sqlc changes: edit `internal/database/queries.sql` then `sqlc generate`
- Cloud Hypervisor requires Linux + KVM. Dev on macOS, test on the OVH bare metal host.
- Use `make dev-up` / `make dev-down` for remote dev (see `contrib/remote/`)

## Docs

Spec, roadmap, and pitch deck live in Obsidian: **`~/Desktop/vault/Kindling/`**
