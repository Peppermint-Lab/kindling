# Private networking (production foundations)

This document records the **chosen direction** for multi-server private networking (`PEP-287`) and what is true in the **shipped binary today** versus the **target** described in the product spec.

## Now (do this before a WireGuard mesh)

For multi-server Kindling today, centralize database access and keep it off the public internet:

1. **One private entry point for SQL** — Run **PgBouncer** (or connect only to a **private Patroni / HA VIP** that is not routable from the Internet). Every Kindling host uses the **same** connection URI.

2. **Private application DSN only** — Provision `/etc/kindling/postgres.dsn` on each server with a host that resolves to an address on your **private network** (VLAN, VPC internal IP, tailnet, future overlay, etc.). Do not point production application DSNs at a publicly routed PostgreSQL listener. (`kindling serve` resolves the DSN via [`internal/bootstrap/dsn.go`](../internal/bootstrap/dsn.go).)

3. **Direct replication DSN when pooling** — If `/etc/kindling/postgres.dsn` points at **PgBouncer**, also provision `/etc/kindling/postgres.replication.dsn` with a **direct PostgreSQL** host:port reachable from every Kindling server. Kindling's WAL listener uses logical replication and cannot run through PgBouncer.

4. **Tighten the topology** — **PostgreSQL** listens on loopback or a private interface only; it accepts connections from PgBouncer and from the direct replication path used by Kindling's WAL listener. **PgBouncer** listens on a private address/port (commonly **6432**) reachable from every Kindling host. Kindling processes use **TCP to the pooler** for normal application traffic and the replication DSN only for WAL/logical replication—not an open public database port.

5. **Pooling semantics** — Kindling uses session-scoped advisory locks; choose PgBouncer `pool_mode` with care. See [high-availability.md](./high-availability.md).

This **private DSN → pooler → Postgres** pattern narrows DB fan-out and blast radius without changing Kindling’s Postgres-backed reconciler model.

## Decision

**Target model:** a Kindling-managed **WireGuard mesh** between all servers in a multi-server cluster (see the product spec section *Private Networking — WireGuard Mesh*). Goals:

- Stable private IPs per server for east-west traffic.
- PostgreSQL and control-plane traffic over the overlay without exposing the database on the public internet.
- A single notion of “the address other servers use to reach this server” (`servers.internal_ip` aligned with the overlay).

**Interim model (until mesh automation ships):** operators MUST provide:

- **Routable TCP access to the database tier** from every Kindling host: in practice **TCP to PgBouncer** (or a private SQL VIP), not necessarily direct **5432** to Postgres. See **Now** above.
- **Edge → worker VM reachability** for the secondary topology: today VM IPs in the database are host-local (TAP). If the edge runs on the control plane and workloads run on remote workers, the operator must publish or tunnel those backends (host IP + port, or an overlay) so the edge can connect. Kindling does not yet configure that automatically.

**Alternative to WireGuard where available:** **L2/VLAN** or existing DC fabric with predictable routing can satisfy the same goals if every Kindling host gets a stable private address reachable from peers. Kindling still needs a single persisted “reachability” address per server in `servers.internal_ip` (or a future dedicated field) consistent with that fabric.

## What Kindling automates vs operator (today)

| Concern | Shipped today | Target (spec) |
|--------|----------------|---------------|
| WireGuard key material | No | Yes — generate, persist, rotate policy TBD |
| Overlay IPs | No | Yes — deterministic allocation per server UUID |
| Peer setup (`wg0`) | No | Yes — dynamic from PostgreSQL registration |
| PostgreSQL only on overlay | No — use DSN in `postgres.dsn` as configured | Yes — DSN points at overlay IP |

## Related docs

- [High Availability and Supported Topologies](./high-availability.md) — topology contract.
- [Internal DNS Runtime](./internal-dns-runtime.md) — worker DNS; cross-server VM DNS deferred.
