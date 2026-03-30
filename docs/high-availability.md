# High Availability and Supported Topologies

Kindling's control plane depends on PostgreSQL as its state store. This guide covers **supported production topologies**, then PostgreSQL resilience and multi-server HA configuration.

## Supported Production Topologies

These are the only topology shapes treated as supported for self-hosted production. Anything else is untested; behavior is undefined (see [Unsupported topologies](#unsupported-topologies-and-deferred-features)).

### Primary: All-in-one single server

All control plane components, the worker runtime, and PostgreSQL run on one host:

- **On this host:** API, edge proxy (TLS), WAL listener, all reconcilers (`deployment`, `build`, `ci_job`, `remote_vm`, `server`, `volume`, `domain`, `instance_migration`, …), worker runtime (Cloud Hypervisor / Apple VZ / crun as detected), PostgreSQL.
- **Typical use:** Small teams, single-node production, dev clusters.
- **DNS:** Operator provides records for `api.*`, `app.*`, `docs.*` (and workload domains) pointing at this host or your load balancer.
- **Storage:** Local state under `/var/lib/kindling` or `KINDLING_STATE_DIR` (and per-product defaults). For Cloud Hypervisor, `KINDLING_CH_SHARED_ROOTFS_DIR` may be local disk or shared storage if you reuse layers across processes on the same host.

### Secondary: Single control plane + remote workers

One host runs the **full control plane** and PostgreSQL. Additional hosts register as **worker-only** nodes (deployment VMs on local runtime):

- **Control plane server:** API, edge, WAL listener, all reconcilers, PostgreSQL. Leader-elected work (e.g. builds) runs on whichever node holds the advisory lock; in this shape that is expected to be the control plane host.
- **Worker servers:** Worker runtime and deployment VMs only — no WAL listener; workers consume shared state from PostgreSQL.
- **Requirements:** Same Postgres **cluster** and **same logical DSN** for every Kindling process (identical URI in `/etc/kindling/postgres.dsn` or equivalent); each server has a **unique** server identity (see [Multi-server requirements](#multi-server-requirements)); low latency to the database tier (recommended **&lt;5 ms** RTT).
- **Multi-server networking (today):** Every Kindling server must reach the **database entrypoint** on the private network—typically **PgBouncer** on **TCP 6432** (or your chosen port), not a public PostgreSQL port. See [private-networking.md](./private-networking.md). VM backend addresses stored for the edge are **host-local** (e.g. TAP); routing from the edge on the control plane to a VM on another host depends on your network (port publishing / operator overlay). See [internal-dns-runtime.md](./internal-dns-runtime.md) for worker DNS and cross-server routing scope.
- **Shared filesystem:** For **multiple workers** using **Cloud Hypervisor**, plan shared storage (e.g. NFS) for `KINDLING_CH_SHARED_ROOTFS_DIR` so OCI layer cache is coherent across workers.

### Operator-managed infrastructure

Kindling does **not** supply: public DNS, load balancers, GitHub webhook reachability, PostgreSQL HA, or your private network between sites. You must provide those.

### Unsupported topologies and deferred features

| Pattern | Why |
|--------|-----|
| PostgreSQL on a host with **no** Kindling binary | WAL listener and migrations expect Kindling on the DB host. |
| Workers only, **no** control-plane-capable server | Workers need WAL-driven updates and leader duties. |
| **Cross-server VM-to-VM** without operator networking | VM IPs are host-local unless you add routing (e.g. WireGuard/VLAN). Kindling does not build a full mesh automatically **today** (see product spec for target mesh). |
| Separate **CI-only** hosts | `ci_job` reconciler runs on the cluster; separate runner hosts are not a supported product shape. |
| **Multi-region** clusters | Not tested; keep PostgreSQL and Kindling in low-latency proximity. |
| **Crun-only** remote workers in the secondary shape | Secondary topology assumes Cloud Hypervisor workers; crun on remote workers is untested in multi-server clusters. |

> **Unsupported** means untested — Kindling may still start; correctness is not guaranteed.

## PostgreSQL High Availability

Kindling supports PostgreSQL HA setups using Patroni or pg_auto_failover. The goal is automatic failover so that Kindling components reconnect to the new primary with minimal disruption.

### Patroni (Recommended)

Patroni manages PostgreSQL replication and automatic failover using a distributed consensus store (etcd, Consul, or ZooKeeper).

**Key settings for Kindling compatibility:**

- Use streaming replication (`hot_standby` on replicas)
- Set `wal_level = logical` to support Kindling's WAL-based listeners
- Patroni's virtual IP / DNS name should be used as Kindling's `DATABASE_URL` host
- Ensure the replication slot name pattern matches Kindling's slot naming (`kindling_listener_*`)

**Example `patroni.yml` fragment:**

```yaml
postgresql:
  parameters:
    wal_level: logical
    max_replication_slots: 20
    max_wal_senders: 10
    hot_standby: on
  replication:
    username: replicator
    password: <secure-password>
    slots:
      kindling_listener_template:
        type: physical
```

**Connection string for Kindling:**

```
postgres://kindling:<password>@patroni-vip:5432/kindling?sslmode=disable
```

Kindling's components will automatically reconnect after a failover. The WAL listener uses exponential backoff (1s to 30s max) to retry connections.

### pg_auto_failover

pg_auto_failover from Citus Data provides a simpler HA setup with one primary and one standby.

**Requirements:**
- PostgreSQL 13+
- `wal_level = logical` on both nodes
- `hot_standby = on` on the standby
- A replication slot for the standby

**Connection string:**

```
postgres://kindling:<password>@pg-auto-failover-primary:5432/kindling?sslmode=disable
```

### Multi-Host Connection Strings

Kindling supports PostgreSQL connection strings with multiple hosts for application-level failover. Use the standard `pgx` multi-host format:

```
postgres://user:pass@host1:5432,host2:5432,host3:5432/kindling?sslmode=disable&target_session_attrs=any&fallback_application_name=kindling
```

**Key parameters:**
- `target_session_attrs=any` — connects to any host (useful for initial connection to the primary)
- `fallback_application_name=kindling` — helps identify Kindling connections in PostgreSQL logs
- After connecting, Kindling always requires a **read-write** connection for writes. Ensure your HA setup routes write connections to the primary.

> **Note:** Logical replication connections (used by Kindling's WAL listener) can only connect to the primary. The multi-host format above works for the main connection pool. The WAL replication connection will connect to whichever host is currently the primary.

### Connection pooling and private DSN (recommended for multi-server)

**Recommended production pattern:** put **PgBouncer** on a **private** address reachable from every Kindling host. All servers use **one application DSN** pointing at the pooler (for example `postgres://kindling:...@10.50.0.5:6432/kindling?sslmode=require` in `/etc/kindling/postgres.dsn`). PostgreSQL itself stays on **loopback or a private interface** behind the pooler—no public `5432`.

Why:

- **Centralizes** connection fan-in from many `kindling serve` processes.
- **Reduces blast radius** compared to exposing Postgres broadly.
- Matches how many teams run self-hosted control planes before introducing a full overlay.

**Pooling mode caveat:** Kindling uses **session-scoped advisory locks** and a **WAL listener** with replication semantics. Transaction pooling can break session semantics if not carefully separated, and logical replication cannot traverse PgBouncer. Operators should keep a **direct PostgreSQL DSN** for replication traffic (for example `/etc/kindling/postgres.replication.dsn`) when `/etc/kindling/postgres.dsn` points at a pooler. Options teams use:

- **Session mode** pool for components that hold advisory locks (or run without PgBouncer for those paths if you split by deployment), **transaction mode** for read-heavy or stateless-style clients, or
- Dedicated PgBouncer **database aliases** / separate pools with documented `pool_mode` per use-case.

Consult PgBouncer docs and Kindling’s actual connection usage in production before choosing `pool_mode = transaction` globally.

**Example `pgbouncer.ini` (shape only—tune for your network):**

```ini
[databases]
kindling = host=127.0.0.1 port=5432 dbname=kindling

[pgbouncer]
; Bind to a private interface in production, not the public Internet.
listen_port = 6432
listen_addr = 10.50.0.5
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
pool_mode = session
max_client_conn = 1000
default_pool_size = 20
```

**Kindling DSN resolution:** `kindling serve` reads `/etc/kindling/postgres.dsn` first, then `~/.kindling/postgres.dsn`. Multi-server clusters should use the **same file contents** on every host, referencing the **pooler** host:port on your private network. If you provision `/etc/kindling/postgres.replication.dsn` (or `~/.kindling/postgres.replication.dsn`), the WAL listener uses that **direct PostgreSQL** DSN instead and falls back to `postgres.dsn` when the replication file is absent.

## Kindling Multi-Server Deployment

Multiple Kindling servers can run simultaneously, sharing state via PostgreSQL:

| Component | Recommended | Notes |
|-----------|-------------|-------|
| API server | Multiple (active-active) | All hit the same PostgreSQL; use a load balancer |
| Worker | Multiple (active-active) | Work-stealing via PostgreSQL advisory locks |
| Edge proxy | Multiple (active-active) | All read routing state from PostgreSQL via WAL notifications |
| WAL listener | Multiple (active-active) | Each creates a temporary replication slot |
| Leader election | Via PostgreSQL advisory lock | Only one server handles cluster-wide duties at a time |

### Multi-Server Requirements

1. **Identical Postgres DSNs** on all servers (same database cluster): same single-line URI in `/etc/kindling/postgres.dsn` (or the path your install uses consistently) for pooled application traffic. If you enable connection pooling, also provision the same direct-primary URI in `/etc/kindling/postgres.replication.dsn` for logical replication. Not all tooling uses the `DATABASE_URL` env var; the server reads DSN files first via `bootstrap.ResolvePostgresDSN`.
2. **Unique server identity:** each host running `kindling serve` gets a UUID on first boot, persisted to `~/.kindling/server-id` by default. There is no `KINDLING_SERVER_ID` environment variable in today’s server path — treat older docs that mention it as obsolete unless you add explicit support.
3. **Network:** every Kindling process must reach the **same private database entrypoint** (recommended: **PgBouncer** on a private IP, often **TCP 6432**). East-west paths for edge-to-remote-worker VM traffic are separate; see [private-networking.md](./private-networking.md).
4. **Local runtime state:** each server keeps its own VM disks and local state (e.g. under `/var/lib/kindling` / `$HOME/.kindling`). Do **not** point multiple unrelated hosts at the same mutable VM backing store unless you use a supported shared-filesystem pattern (e.g. `KINDLING_CH_SHARED_ROOTFS_DIR` for layer cache only, per topology notes above).

### Bootstrap Sequence

Recommended order for a control-plane-plus-workers cluster:

1. Provision PostgreSQL and a **private SQL entrypoint** (recommended: PgBouncer on a private IP such as `10.50.0.5:6432`).
2. Write the **same** pooled DSN to `/etc/kindling/postgres.dsn` on every host.
3. If the pooled DSN goes through PgBouncer, write the **same** direct-primary DSN to `/etc/kindling/postgres.replication.dsn` on every host.
4. Bring up the **control plane** first (`kindling@edge`, `kindling@api`, `kindling@worker` or equivalent).
5. Verify the first server registers and remains stable across restart (`~/.kindling/server-id` should persist).
6. Join additional workers with the same DSN set and a **non-loopback** internal IP.

Kindling now rejects common unsafe join states, including:

- multi-server registration with `127.0.0.1` / `0.0.0.0` / `localhost` as the worker internal address
- multi-server startup with a local-only database DSN (`localhost`, `127.0.0.1`, local socket)

### Validation Checklist

- `GET /api/meta` succeeds on the public API origin
- `GET /api/servers` shows the expected control-plane and worker nodes
- every node uses the same `/etc/kindling/postgres.dsn`
- if pooling, every node uses the same `/etc/kindling/postgres.replication.dsn`
- every worker has a stable `server-id`
- joined workers report healthy component metadata
- the control plane can proxy to a workload running on a joined worker
- if pooling, WAL listener logs stay clean against the direct replication DSN

### Server Heartbeat and Dead Server Detection

Kindling detects dead workers via a heartbeat mechanism:

- Each worker writes a heartbeat row every **10 seconds** (`servers.last_heartbeat_at`)
- A server is considered **dead** if no heartbeat is seen for **30 seconds**
- Dead server detection triggers:
  - Deployment instance recovery (instances on the dead server are re-provisioned)
  - Build orphan recovery (builds claimed by the dead server are reset to pending)
  - Sandbox cleanup

### Graceful Shutdown

Set `KINDLING_PRESERVE_WORKLOADS_ON_SHUTDOWN=1` to keep VMs running when a worker shuts down. The VMs will be picked up by another worker after restart.

## Alerting Recommendations

Key metrics to monitor for HA:

| Signal | Threshold | Action |
|--------|-----------|--------|
| PostgreSQL connection errors | Any | Check database availability |
| WAL listener reconnect frequency | > 1/minute | Investigate network or database issues |
| Server heartbeat missing | > 30s | Check worker process and network |
| Build stuck in `building` | > 15 min | Check build worker and GitHub connectivity |
| Deployment instance restart loop | > 3 restarts in 5 min | Check application health and resources |