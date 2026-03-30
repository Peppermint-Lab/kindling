# High Availability and Supported Topologies

Kindling's control plane depends on PostgreSQL as its state store. This guide covers making the PostgreSQL layer resilient, configuring Kindling for HA deployments, and the explicitly supported production topologies.

## Supported Production Topologies

Kindling supports two production topology shapes. Running in configurations not listed here is out of scope for this milestone (PEP-278).

### Primary: All-in-One Single Server

All Kindling components and PostgreSQL run on a single host:

- **API + Edge + WAL Listener + Reconcilers + Worker + PostgreSQL**: everything on one machine
- **Recommended for**: small teams, single-node production clusters, development clusters
- **PostgreSQL HA**: Patroni or pg_auto_failover may coexist on this host for DB-level failover
- **Public ingress**: `api.kindling.systems`, `app.kindling.systems`, `docs.kindling.systems` all resolve to this host
- **Scaling**: scale the single host vertically; multi-server clustering is available via the secondary topology

```
┌─────────────────────────────────────────────────────────────┐
│  api.kindling.systems / app.kindling.systems / docs.*      │
│                           │                                 │
│  ┌────────────────────────┴──────────────────────────────┐  │
│  │             Edge Proxy (CertMagic TLS, port 443)     │  │
│  └────────────────────────┬──────────────────────────────┘  │
│                           │                                  │
│  ┌────────────────────────┴──────────────────────────────┐  │
│  │  API  │  Reconcilers  │  WAL Listener  │  Worker     │  │
│  └────────────────────────┬──────────────────────────────┘  │
│                           │                                  │
│  ┌────────────────────────┴──────────────────────────────┐  │
│  │              PostgreSQL (local, Patroni optional)       │  │
│  └─────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Secondary: Single Control Plane + Remote Workers

One server runs the full control plane (API, edge, WAL listener, all reconcilers) and PostgreSQL. Additional servers register as worker-only nodes:

- **Control plane server**: API, edge proxy, WAL listener, all reconcilers (including build), PostgreSQL
- **Worker servers**: local runtime (Cloud Hypervisor or crun), deployment VMs only — no WAL listener, no build reconciler
- **Recommended for**: teams needing to spread workload VMs across multiple physical hosts while keeping operational complexity low
- **Builds always run on the control plane server** (the leader); worker servers only run deployment VMs

```
┌─────────────────────────────┐    ┌─────────────────────────────────┐
│   Control Plane Server       │    │   Worker Server 1               │
│  API + Edge (TLS 443)       │    │  Local runtime (CH/crun)        │
│  Reconcilers + WAL Listener │    │  Deployment VMs                 │
│  PostgreSQL                  │    │  Note: no WAL listener here     │
│  (all build execution)       │    └─────────────────────────────────┘
└─────────────────────────────┘                │
                                                  ┌─────────────────────────────────┐
   All servers share the same                     │   Worker Server N               │
   DATABASE_URL and must reach                    │  Local runtime (CH/crun)        │
   PostgreSQL on port 5432.                        │  Deployment VMs                 │
   Each server has a unique                        └─────────────────────────────────┘
   KINDLING_SERVER_ID (auto-generated).
```

---

## Port and Storage Summary

### Ports

| Port | Direction | Purpose |
|------|-----------|---------|
| 443 | Inbound (public) | TLS termination for all public hostnames (api, app, docs, workload domains) |
| 80 | Inbound (public) | HTTP-01 ACME challenge for TLS cert issuance/renewal |
| 5432 | Outbound (server to DB) | PostgreSQL client connections from Kindling to the database host |
| 22 | Outbound | Git clone from GitHub (outbound SSH or HTTPS) |

In the **secondary topology**, additional ports are used:

| Port | Direction | Purpose |
|------|-----------|---------|
| Any | Worker to control plane | PostgreSQL connections from workers to the control plane server's DB |
| Worker host IP + published port | Edge to workload | Edge proxies to VM backends via host-level port forwarding |

PostgreSQL itself should **not** be exposed to the public internet. It should bind to loopback (`127.0.0.1`) or be protected by a firewall that allows only Kindling server IPs.

### Storage

| Path | Purpose | Shared across servers? |
|------|---------|----------------------|
| `/var/lib/kindling` (or `$HOME/.kindling`) | Server ID, runtime state, VM disks (qcow2) | No — each server has local state |
| `KINDLING_CH_SHARED_ROOTFS_DIR` | OCI image layer cache for Cloud Hypervisor | Yes — must be shared (NFS or similar) when using Cloud Hypervisor with multiple servers |
| PostgreSQL data directory | All Kindling state | Managed by PostgreSQL; Patroni/HA setups manage replication |

### Network Assumptions

- All Kindling servers must have outbound access to GitHub (TCP 443, port 22) to clone repos
- All Kindling servers must have TCP access to PostgreSQL on the database host (port 5432)
- The edge proxy binds to all interfaces (0.0.0.0) on port 443 and 80
- Workers are reached by the edge via `vm_ip:port` entries in PostgreSQL — these are host-local TAP addresses and are not routable across server boundaries without operator-managed networking (WireGuard, VLAN)
- All servers must have low-latency access to PostgreSQL (recommended <5ms RTT)

### DNS Requirements

Operator must provision the following DNS records pointing to the control plane server (or load balancer in front of it):

| Hostname | Purpose |
|----------|---------|
| `api.kindling.systems` | REST/Connect API + webhooks |
| `app.kindling.systems` | Dashboard SPA |
| `docs.kindling.systems` | Documentation |
| `*.kindling.systems` (wildcard) | Service-generated hostnames (`<service>-<project>.<service_base_domain>`) |
| `*.preview.kindling.systems` (wildcard) | Preview deployment hostnames |

---

## Unsupported Topologies

The following are explicitly out of scope for this milestone. Behavior when running in these configurations is undefined.

| Pattern | Reason |
|---------|--------|
| PostgreSQL on a host without a Kindling binary | The WAL listener must run on the same host as PostgreSQL to consume logical replication. A separate WAL consumer host is not supported. |
| Worker-only cluster with no control plane server | Worker nodes depend on the control plane server for WAL notifications and leader-elected reconcilers. A cluster of workers without any control plane node is not supported. |
| Cross-server workload-to-workload routing | VM IP addresses are host-local TAP addresses. There is no built-in cross-server routing. This requires operator-managed networking outside Kindling's scope. |
| Separate CI runner hosts | CI job execution runs on the control plane server via the `ci_job` reconciler. Independent CI-only hosts are not supported. |
| Multi-region deployments | All servers must have low-latency access to PostgreSQL (<5ms RTT recommended). Cross-datacenter clustering is untested. |

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

### Connection Pooling

For multi-server Kindling deployments, use **PgBouncer** in front of PostgreSQL:

- Transaction pooling mode is recommended (lower memory footprint)
- Kindling's advisory locks require a consistent session, so set `pool_mode = transaction` carefully — the reconciler and WAL components use `pg_advisory_lock` which is session-scoped
- Use session pooling for the advisory lock connections, or transaction pooling with `pg_advisory_lock` calls using one connection per holder

**Example `pgbouncer.ini`:**

```ini
[databases]
kindling = host=patroni-vip port=5432 dbname=kindling

[pgbouncer]
listen_port = 6432
listen_addr = 0.0.0.0
auth_type = md5
auth_file = /etc/pgbouncer/userlist.txt
pool_mode = transaction
max_client_conn = 1000
default_pool_size = 20
```

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

1. **Identical `DATABASE_URL`** on all servers (same database cluster)
2. **`KINDLING_SERVER_ID`** must be unique per server (generated once per server and persisted to disk)
3. **Network:** all servers must be reachable from each other on the PostgreSQL port (5432)
4. **Shared state:** all servers share the same `/var/lib/kindling` state directory if using local runtime (or use a shared filesystem for retained VM state)

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