# Kindling security architecture overview

## Request flow

- Public traffic terminates at `internal/edgeproxy`, which owns HTTP→HTTPS redirects, TLS, host-based routing, and reverse proxying.
- For control-plane hosts, the edge proxy forwards requests to the loopback API backend (`cmd/kindling/serve_edge.go` → `cmd/kindling/serve_http.go`).
- Current forwarding behavior: both control-plane and app proxy paths strip inbound `X-Forwarded-For`, `X-Real-IP`, and `Forwarded`, then set edge-controlled forwarding headers in ReverseProxy `Rewrite` handlers (`internal/edgeproxy/edgeproxy.go`), so backend authorization/rate-limit logic should rely only on these sanitized values.
- Inside the API process, requests pass through CORS handling, then targeted auth-route rate limiting (`POST /api/auth/login` and `POST /api/auth/bootstrap`), then `internal/auth.Middleware`, then the `http.ServeMux`.
- The mux exposes a small public surface (`/healthz`, `/`, `/webhooks/github`) plus the REST API registered by `internal/rpc`.
- RPC handlers are grouped by domain (projects, deployments, auth, domains, servers, volumes, CI) and use request-scoped principals plus role checks for authorization.

## Auth and CSRF model

- `internal/auth.PublicRoute` defines the unauthenticated routes: health checks, webhook ingress, bootstrap/login/logout/session endpoints, and external auth entry/callback routes.
- All other API routes go through `internal/auth.Middleware`.
- The middleware accepts either:
  - browser sessions via the `kindling_session` cookie, or
  - bearer API keys with the Kindling API-key prefix.
- Successful authentication loads the user, org membership, and role from PostgreSQL and attaches an auth principal to the request context.
- State-changing cookie-authenticated requests must also pass trusted-origin checks (`Origin`/`Referer`) to block CSRF; allowed origins come from the current request origin plus configured dashboard/public hosts.
- Handlers then perform finer-grained authorization such as org-admin or platform-admin checks.

## Build and CI pipeline

- GitHub sends webhook traffic to `POST /webhooks/github`, handled by `internal/webhook`.
- Production push and preview PR webhooks verify the per-project webhook secret before creating deployment records.
- Deployment reconciliation creates build records as needed; the builder then downloads the repository tarball from GitHub, prepares a build context, builds an OCI image with `buildah`, and pushes it to the configured registry.
- Successful builds create image records in PostgreSQL; deployment reconciliation then rolls those images out onto runtime servers.
- GitHub Actions `workflow_job` webhooks follow a similar ingress path, validate an org integration secret, create/update `ci_jobs`, and hand execution off to the CI job service and reconciler.

## Secrets storage and access

- A 32-byte master key is loaded from `/etc/kindling/master.key` or a local development key under `~/.kindling/master.key`.
- Cluster-wide secrets (for example GitHub and registry credentials) are encrypted with that master key and stored in PostgreSQL `cluster_secrets`.
- `internal/config.Manager` decrypts cluster secrets into an in-memory snapshot used by the API, builder, CI, and worker components.
- Project and service env vars are stored in the database as encrypted envelopes; API handlers encrypt on write and deployment code decrypts only when building the runtime environment for a deployment.
- Secret values are intentionally write-only at the API layer; list endpoints return metadata, not plaintext.

## Security-relevant packages

- `internal/edgeproxy`: TLS termination, host routing, reverse proxying, control-plane/API forwarding.
- `internal/auth`: session and API-key auth, principal injection, trusted-origin / CSRF enforcement.
- `internal/rpc`: REST handlers and authorization checks for control-plane operations.
- `internal/webhook`: GitHub webhook ingress and signature verification.
- `internal/builder`: source fetch, Dockerfile detection, OCI image build and registry push.
- `internal/ci`: workflow-job intake and execution orchestration for Kindling-managed runners.
- `internal/config` + `internal/bootstrap`: master-key loading, secret encryption/decryption, live config snapshots.
- `internal/deploy`: deployment reconciliation, image selection, and decrypted runtime env assembly.
- `internal/database`: PostgreSQL access and typed queries used across auth, config, build, and deploy paths.
