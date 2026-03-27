import { loginHref } from "@/lib/login"

export function Hero() {
  return (
    <section className="mx-auto max-w-3xl px-6 pt-20 pb-16">
      <h1 className="text-4xl sm:text-5xl font-bold tracking-tight leading-[1.1]">
        Self-hosted PaaS on microVMs
      </h1>

      <p className="mt-6 text-lg text-gray-600 leading-relaxed max-w-2xl">
        Push code, get production. Kindling gives you Railway&rsquo;s developer
        experience with Coolify&rsquo;s self-hosting
        ethos&nbsp;&mdash;&nbsp;but every deployment runs in its own Cloud
        Hypervisor microVM, not a container. Runs on any Linux server with KVM.
        $5 VPS to bare metal.
      </p>

      <p className="mt-4 text-lg text-gray-600 leading-relaxed max-w-2xl">
        Open source. Free forever. No vendor lock-in.
      </p>

      <div className="mt-8 flex flex-wrap items-center gap-3">
        <a
          href="https://docs.kindling.dev"
          className="bg-black text-white text-sm font-medium px-4 py-2 rounded-md hover:bg-gray-800 transition-colors"
        >
          Get started
        </a>
        <a
          href={loginHref}
          className="text-sm font-medium px-4 py-2 rounded-md border border-gray-300 text-gray-800 hover:border-gray-500 hover:text-black transition-colors"
        >
          Sign in
        </a>
        <a
          href="https://github.com/kindlingvm/kindling"
          className="text-sm text-gray-500 hover:text-black transition-colors"
        >
          View on GitHub &rarr;
        </a>
      </div>
    </section>
  )
}
