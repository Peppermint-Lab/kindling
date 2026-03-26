export function Features() {
  return (
    <section id="features" className="mx-auto max-w-3xl px-6 py-16 border-t border-gray-200">
      <h2 className="text-2xl font-bold tracking-tight">Why Kindling</h2>
      <p className="mt-2 text-gray-500 text-sm">
        Everything you need to deploy apps on your own hardware. No Kubernetes, no Docker Compose, no YAML.
      </p>

      <dl className="mt-8 space-y-6 text-[15px]">
        <div>
          <dt className="font-semibold">Git push deploy</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            Push to main for production. Open a PR for a preview environment.
            GitHub webhooks trigger builds automatically. Zero CI config.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Sub-500ms microVM boot</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            Cloud Hypervisor launches VMs in under 500ms with less than 5MB
            memory overhead. Scale to zero without paying for idle. Wake on
            first request.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Isolated builds</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            Every build runs in its own environment via buildah/podman. No
            shared Docker daemon, no cross-tenant leaks.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Framework detection</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            No Dockerfile? Kindling detects Next.js, Nuxt, Rails, Laravel, and
            Go from file signatures and injects an optimized multi-stage
            Dockerfile.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Automatic TLS</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            CertMagic provisions Let&rsquo;s Encrypt certificates on first HTTPS
            request via TLS-ALPN-01. Custom domains work out of the box. Certs
            stored in PostgreSQL, shared across nodes.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Declarative reconcilers</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            All state transitions are driven by reconcilers, not imperative
            pipelines. PostgreSQL WAL listener triggers reconciliation in
            real-time. Failed? Retry in 5 seconds. Dead server? VMs failover
            automatically.
          </dd>
        </div>
        <div>
          <dt className="font-semibold">Single binary, Postgres only</dt>
          <dd className="mt-1 text-gray-600 leading-relaxed">
            One Go binary. PostgreSQL for state, coordination, and leader
            election (advisory locks). No etcd, no Raft, no external consensus
            system.
          </dd>
        </div>
      </dl>
    </section>
  )
}
