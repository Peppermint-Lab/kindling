const faqs = [
  {
    q: "Do I need KVM?",
    a: "No. Kindling auto-detects your host runtime. Linux with /dev/kvm uses Cloud Hypervisor microVMs. Linux without KVM falls back to crun (OCI containers). macOS uses Apple Virtualization Framework for local development. Same deploy pipeline, same API.",
  },
  {
    q: "What frameworks are auto-detected?",
    a: "Next.js, Nuxt, Rails, Laravel, and Go. If Kindling finds the right file signatures (nuxt.config.ts, next.config.js, Gemfile + Rakefile, artisan, go.mod) it injects an optimized multi-stage Dockerfile. You can always provide your own.",
  },
  {
    q: "How is this different from Coolify?",
    a: "Coolify runs your apps in Docker containers with Traefik for routing. Kindling runs each deployment in its own Cloud Hypervisor microVM with full kernel-level isolation, uses CertMagic for on-demand TLS, and drives all state transitions through declarative reconcilers instead of imperative scripts.",
  },
  {
    q: "What's the minimum server requirement?",
    a: "Any Linux server. A $5 VPS works for small projects. For microVM isolation you need KVM support. Without KVM, Kindling falls back to crun containers automatically. You need buildah or podman in PATH for builds.",
  },
  {
    q: "How does the reconciler architecture work?",
    a: "Every entity (deployment, build, instance, VM, domain, server) has a reconcile function. When a database row changes, the PostgreSQL WAL listener picks it up via logical replication and schedules the relevant reconciler. The reconciler reads current state and converges toward desired state. Failed reconciliations retry after 5 seconds.",
  },
  {
    q: "Is it production-ready?",
    a: "Kindling is pre-1.0. The core deploy pipeline works: git push to build to microVM to live URL with TLS. Horizontal scaling, dead server detection, and custom domains are implemented. Auth, secrets management, and multi-server networking are on the roadmap.",
  },
  {
    q: "What's the tech stack?",
    a: "Go single binary. PostgreSQL for state and coordination. Cloud Hypervisor (rust-vmm) for microVMs. CertMagic for TLS. buildah/podman for OCI image builds. React + Vite + Tailwind for the dashboard. Connect RPC + REST API. OpenTelemetry for tracing.",
  },
]

export function FAQ() {
  return (
    <section id="faq" className="mx-auto max-w-3xl px-6 py-16 border-t border-gray-200">
      <h2 className="text-2xl font-bold tracking-tight">FAQ</h2>

      <div className="mt-8 space-y-8">
        {faqs.map((f) => (
          <div key={f.q}>
            <h3 className="font-semibold text-[15px]">{f.q}</h3>
            <p className="mt-2 text-sm text-gray-600 leading-relaxed">
              {f.a}
            </p>
          </div>
        ))}
      </div>
    </section>
  )
}
