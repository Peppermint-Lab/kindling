# Architecture

Kindling is a self-hosted PaaS built around declarative convergence. Rather than running a single imperative deploy pipeline, it stores desired state in PostgreSQL and continuously reconciles the system toward that state.

## Control Plane Model

At a high level, Kindling consists of:

- a Go control plane binary
- PostgreSQL for state, coordination, and notifications
- reconcilers for deployments, builds, instances, domains, and servers
- an edge proxy for hostname routing and TLS
- one or more workload hosts that run application instances locally

Every server runs the same binary. One server becomes the cluster leader for shared duties, while workload reconciliation also happens on the relevant hosts.

## Reconcilers And WAL-Triggered Convergence

Kindling uses PostgreSQL logical replication to react to row changes.

The flow is:

1. A row changes in PostgreSQL.
2. The WAL listener receives the change.
3. The matching reconciler is scheduled.
4. The reconciler reads current state and desired state.
5. It converges the system toward the target state.
6. Failures retry after a short delay rather than requiring a separate recovery path.

This model drives builds, deployments, deployment instances, VM lifecycle, domain routing, and server-state transitions.

## Leader Election

Leader election uses PostgreSQL session-scoped advisory locks.

- one server acquires the leader lock
- the leader handles cluster-wide duties such as shared build/deploy coordination and dead-server handling
- if the session drops, the lock is released automatically and another server can take over

That keeps coordination inside PostgreSQL rather than introducing a separate consensus system.

## Runtime Matrix

Kindling chooses the best runtime available on the current host.

| Host environment | Runtime | Notes |
| --- | --- | --- |
| Linux with KVM | Cloud Hypervisor | Primary isolation path using microVMs |
| macOS development | Apple Virtualization | Linux microVM path for local development and builder VM workflows |
| Linux without KVM | `crun` | OCI container fallback with the same control-plane model |

The deploy pipeline stays the same even when the runtime changes.

## Deployment Lifecycle

At a high level:

1. A deployment request arrives from GitHub/webhook, the API, or the dashboard.
2. The deployment reconciler creates or advances a build.
3. The build logic downloads source, detects a framework or uses the provided Dockerfile, and produces an OCI image.
4. The deployment reconciler creates deployment instance rows for the desired replica count.
5. Instance reconciliation starts workloads on the selected host using the best available runtime.
6. Readiness is established through the runtime-specific path.
7. The edge proxy updates routing so healthy backends receive traffic.
8. Later state changes such as scaling, drain, or cleanup are handled by the same reconciliation model.

## Related Docs

- [Getting Started on Linux](getting-started-linux.md)
- [Development](development.md)
- [Internal DNS Runtime](internal-dns-runtime.md)
- [Root README](../README.md)
