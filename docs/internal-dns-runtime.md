# Internal DNS Runtime

Kindling workers now provide an internal DNS service for hosted `cloud-hypervisor` workloads.

Current behavior:

- Hosted Linux guests use the worker gateway as their first resolver.
- Names under `*.kindling.internal` are enforced at runtime.
- `prod` workloads can resolve only in-org `prod` service names.
- Preview workloads can resolve only their exact preview environment within the same project.
- Non-internal names are forwarded to upstream public resolvers.
- If a target service has no healthy running backends yet, the worker returns `SERVFAIL` instead of leaking stale or cross-environment routes.

Worker configuration:

- `KINDLING_INTERNAL_DNS_ADDR`
  - default: `:53`
  - set to `off`, `disabled`, or `false` to disable the runtime DNS server
- `KINDLING_INTERNAL_DNS_UPSTREAMS`
  - comma-separated upstream resolvers
  - default: `1.1.1.1:53,8.8.8.8:53`

Operational notes:

- The worker heartbeat metadata now includes `internal_dns_enabled` and, when enabled, `internal_dns_addr`.
- The current runtime implementation resolves internal names to active backend VM IPv4 addresses.
- This delivers policy enforcement and same-runtime connectivity now; cross-server private routing is still the next networking milestone. See [private-networking.md](./private-networking.md) for the multi-server overlay decision and interim operator requirements.
