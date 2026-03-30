# High Availability

Kindling's control plane depends on PostgreSQL as its state store. This guide covers making the PostgreSQL layer resilient and configuring Kindling for HA deployments.

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