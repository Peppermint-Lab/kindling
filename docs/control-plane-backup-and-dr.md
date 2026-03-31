# Control-plane backup and disaster recovery

This document is the **operator-facing contract** for what must be protected to recover a Kindling cluster after database loss, host loss, or corruption. It complements [high-availability.md](./high-availability.md) (topology and Postgres HA) and [private-networking.md](./private-networking.md).

## What is *not* control-plane backup

**Project persistent volume cold backup / restore** (S3-compatible object storage, per-project volume APIs) protects **application data** under Kindling’s volume model. It does **not** replace backup of the **PostgreSQL database** that holds Kindling’s control-plane state. Operators must plan both:

| Concern | Scope | Typical mechanism |
|--------|--------|-------------------|
| **Control-plane state** | Rows in PostgreSQL: projects, deployments, servers, settings, encrypted secrets, volume *metadata*, audit tables, etc. | Postgres backup + restore (or replica failover per HA guide) |
| **Workload data on volumes** | Files inside guest-attached volumes | Kindling volume backup APIs / object storage |
| **Host-local runtime state** | VM disks, layer caches, `server-id`, WireGuard private keys | Rebuild or restore from your own imaging / config management |

## Authoritative state (must be protected for DR)

1. **PostgreSQL database** used by every `kindling serve` process (same logical DB cluster for all nodes). This includes at minimum:
   - All application tables (deployments, instances, domains, routing, users, orgs, etc.)
   - **`cluster_settings`** — public URLs, domains, preview settings, and other cluster keys
   - **`cluster_secrets`** — ciphertext for cluster-wide secrets (e.g. volume backup credentials)
   - **`certmagic_data`** — TLS certificate storage when using in-DB CertMagic
   - **`environment_variables`** and related encrypted envelopes for project/service secrets
   - **Logical replication** metadata Kindling relies on (`wal_level = logical`, listener slots — see HA guide)

2. **Connection configuration on every host** (outside the database but required to reattach):
   - `/etc/kindling/postgres.dsn` (and `/etc/kindling/postgres.replication.dsn` when using PgBouncer)
   - Same contents on all nodes in a multi-server cluster

3. **Per-server identity and mesh keys** (not in Postgres backups unless you restore the same DB):
   - `~/.kindling/server-id` — must remain stable per physical host across restarts; new hosts get new IDs
   - `~/.kindling/wg-private-key` (or `KINDLING_WG_PRIVATE_KEY`) when WireGuard mesh is enabled — **per-node**; loss requires mesh re-keying / operator coordination per [private-networking.md](./private-networking.md)

4. **Local VM and runtime directories** (see HA guide for `KINDLING_STATE_DIR`, `KINDLING_CH_SHARED_ROOTFS_DIR`, etc.):
   - Losing a worker host loses local VM disks unless you rely on shared storage patterns documented for multi-worker Cloud Hypervisor
   - Recovery redeploys control-plane *intent* from Postgres; **guest filesystems** may need volume backups or external replication

## RPO / RTO (planning assumptions)

Exact numbers are **operator-defined**. Kindling does not ship automated scheduled **control-plane** backups today. Use this as a planning baseline:

- **RPO (control plane):** bounded by your Postgres backup cadence and WAL archiving policy (if any). Without point-in-time recovery, RPO is the interval between full backups.
- **RTO:** bounded by time to provision a new primary, restore backup, repoint DSNs, restart `kindling serve`, and validate routing.
- **RPO/RTO (volumes):** governed by Kindling volume backup frequency and object-store retention (scheduled volume backups are product roadmap; manual/cold backup exists today).

## Recovery ordering (high level)

1. Restore **PostgreSQL** to a consistent state (primary reachable, `wal_level` and replication compatible with Kindling — see [high-availability.md](./high-availability.md)).
2. Ensure **DSN files** on each Kindling host match the restored cluster entrypoints.
3. Start **control plane** processes (`api`, `edge`, `worker` as per your topology); allow migrations to reconcile.
4. Reconcile **server registration**: hosts should retain stable `server-id` where possible; new hardware gets new IDs and may need operator follow-up for mesh coordination env vars.
5. Validate: `GET /api/meta`, `GET /api/servers`, deploy a test workload, verify edge → worker routing per networking doc.
6. Restore **application data** via volume restore APIs where needed — independent of control-plane DB restore.

## Drills

Production teams should periodically:

- Restore Postgres to a non-production instance and verify Kindling starts and passes the validation checklist in [high-availability.md](./high-availability.md).
- Perform a **volume** restore drill for a non-production project and verify data integrity.

## Related documentation

- [high-availability.md](./high-availability.md) — supported topologies, Patroni / pg_auto_failover, PgBouncer + replication DSN
- [private-networking.md](./private-networking.md) — WireGuard mesh and `servers` persistence
- [cluster-audit-events.md](./cluster-audit-events.md) — audit trail for high-risk admin actions after recovery
- Operator runbooks: `web/docs/operate/` (backup-and-restore, operations-runbook)
