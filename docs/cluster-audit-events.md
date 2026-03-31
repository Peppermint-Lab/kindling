# Cluster audit events (design)

Kindling records **cluster-global audit events** for high-risk administrative actions that can affect the entire deployment. This is separate from:

- **`remote_vm_access_events`** — shell/SSH/exec/copy access to remote VMs (narrow, VM-scoped; see schema).
- **Application logs** — operational noise, not a durable compliance trail.

## Goals

- Provide a **durable, queryable** record of *who* changed *what* for platform-admin operations.
- **Never** store secrets, tokens, or raw `client_secret` values in audit `details`.
- Keep the first implementation **small**: platform-admin HTTP mutations only.

## Storage

Table: **`cluster_audit_events`** (see [internal/database/schema.sql](../internal/database/schema.sql))

| Column | Purpose |
|--------|---------|
| `user_id` | Authenticated user (nullable if future system actions) |
| `action` | Stable verb namespace, e.g. `server.drain`, `cluster.settings.update` |
| `resource_type` | e.g. `server`, `cluster`, `auth_provider` |
| `resource_id` | Stable string identifier (e.g. server UUID, provider name) |
| `details` | JSON object: non-secret summary (fields changed, flags, normalized URLs where safe) |
| `request_ip` | Remote address from request (best effort; may reflect proxy hop) |
| `user_agent` | Client `User-Agent` header |

## Actions in the first slice

| `action` | `resource_type` | `resource_id` | Notes |
|----------|-----------------|---------------|-------|
| `server.drain` | `server` | server UUID | After successful drain |
| `server.activate` | `server` | server UUID | After successful activate |
| `cluster.settings.update` | `cluster` | empty | `details.changed` lists which persisted meta keys changed (values are non-secret cluster settings) |
| `auth.provider.update` | `auth_provider` | `github` / `oidc` | `details` includes `enabled`, `display_name`, `issuer_url_set`, `client_id_nonempty`, `client_secret_rotated`, `client_secret_cleared` — **never** the secret |

## Read API / UI

**Deferred:** listing audit events in dashboard or public API is not required for the first write-path milestone. Operators can query Postgres directly (`SELECT * FROM cluster_audit_events ORDER BY created_at DESC LIMIT …`) with appropriate access controls.

## Implementation notes

- Audit writes are **best-effort on failure**: if insert fails, the handler logs a warning and the primary operation still succeeded. Future work can tie audit + state in a transaction where justified.
- `details` must be JSON-serializable and kept small.
