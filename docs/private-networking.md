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

**Without Kindling mesh:** operators MUST provide:

- **Routable TCP access to the database tier** from every Kindling host (typically **TCP to PgBouncer** or a private SQL VIP). See **Now** above.
- **Edge → worker VM reachability** for the secondary topology: VM IPs in Postgres are host-local (TAP). You must publish, tunnel, or route so the edge host can open TCP to those backends—unless you enable **Kindling WireGuard mesh** on Linux, which adds kernel routes for peer **`ip_range`** blocks over `wg0`.

**Alternative to WireGuard where available:** **L2/VLAN** or existing DC fabric can replace the overlay if every Kindling host has a stable private underlay. Persist reachability in `servers.internal_ip` consistent with that fabric.

## What Kindling automates vs operator (today)

| Concern | Shipped today | Target (spec) |
|--------|----------------|---------------|
| WireGuard key material | Optional — when `KINDLING_WG_MESH=1` on Linux (see below) | Yes — generate, persist, rotate policy TBD |
| Overlay IPs | Optional — deterministic per `server_id` when mesh enabled | Yes — deterministic allocation per server UUID |
| Peer setup (`wg0`) | Optional — `wg0` on Linux when mesh enabled | Yes — dynamic from PostgreSQL registration |
| PostgreSQL only on overlay | No — use DSN in `postgres.dsn` as configured | Yes — DSN points at overlay IP |

## Frozen model (PEP-287) — implementation contract

This section is the **authoritative contract** for Production Foundations milestone work. Product spec (`Private Networking — WireGuard Mesh`) remains the narrative; this doc is the operator + schema contract.

### Primary private path

- **Kindling-managed WireGuard** on **`wg0`** is the **default** automation path for multi-server east-west connectivity on **Linux** control-plane and worker hosts.
- **VLAN / L2** remains a valid **operator-managed** alternative: you may disable Kindling’s mesh (`KINDLING_WG_MESH` unset or `0`) and provide your own routable underlay. You must still satisfy the **edge → VM backend** requirement below.

### Persistence (`servers` table)

| Column | Meaning |
|--------|---------|
| `internal_ip` | **Reachability for cluster TCP** as consumed by Kindling: with mesh enabled, this is the server’s **WireGuard overlay IPv4** (`10.64.x.x`). Without mesh, it is the host’s stable private IPv4 used for cross-host traffic (often set explicitly via `KINDLING_INTERNAL_IP`). |
| `wireguard_ip` | Overlay address (same value as `internal_ip` when mesh is on; `0.0.0.0` when mesh is off). Stored as `INET`. |
| `wireguard_public_key` | This server’s WireGuard **public** key (base64), empty when mesh is off. |
| `wireguard_endpoint` | **`host:port`** UDP endpoint **other peers use** to reach this node’s WireGuard listener (underlay), e.g. `203.0.113.10:51820`. Empty when mesh is off or not yet published. |

Private key material is **per-node**. Resolution order: **`KINDLING_WG_PRIVATE_KEY`** override, then local file **`~/.kindling/wg-private-key`**, else generate a new local key on first boot. Kindling does **not** share WireGuard private keys through cluster-wide secrets.

### Environment (mesh)

| Variable | Required | Purpose |
|----------|----------|---------|
| `KINDLING_WG_MESH` | To enable | Set to `1` to turn on Linux mesh automation for this process. |
| `KINDLING_WG_ENDPOINT` | Multi-server mesh | This host’s UDP endpoint as seen by peers (`ip:port`). |
| `KINDLING_WG_LISTEN_PORT` | Optional | UDP listen port (default **51820**). |
| `KINDLING_WG_PRIVATE_KEY` | Optional | Override generated/stored private key (base64). |
| `KINDLING_COORDINATION_SERVER_WG_IP` | Joining hosts | Coordination peer overlay IP (`10.64.x.x`). |
| `KINDLING_COORDINATION_SERVER_ENDPOINT` | Joining hosts | Coordination peer UDP endpoint `host:port`. |
| `KINDLING_COORDINATION_SERVER_PUBKEY` | Joining hosts | Coordination peer public key (base64). |

First cluster node omits coordination variables; joining nodes include them so they can bootstrap **one** initial peer before full mesh convergence.

### Edge → workload backends (PEP-287 / routing)

- The edge proxy still reads **`vm_ip` + port** from PostgreSQL and opens TCP to those literals ([`internal/edgeproxy`](../internal/edgeproxy/edgeproxy.go)).
- With **mesh enabled**, Kindling configures WireGuard **`AllowedIPs`** for each peer to include that peer’s **`servers.ip_range`** (VM / TAP CIDR for that host). That gives the **control-plane / edge host** kernel routes to remote VM addresses over `wg0`, so unchanged `vm_ip` values can work **when** those IPs live inside the peer’s allocated `ip_range`.
- If mesh is **disabled**, operators must provide **their own** routing, tunneling, or publishing so the edge host can open TCP to worker `vm_ip:port`.

### Non-Linux

- WireGuard automation is **skipped** on non-Linux builds and on macOS dev hosts; use operator networking or single-node topologies.

## Related docs

- [High Availability and Supported Topologies](./high-availability.md) — topology contract.
- [Internal DNS Runtime](./internal-dns-runtime.md) — worker DNS; cross-server VM DNS deferred.
- [Control-plane backup and disaster recovery](./control-plane-backup-and-dr.md) — Postgres vs host-local vs volume scope.
