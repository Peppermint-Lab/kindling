export function Terminal() {
  return (
    <section className="mx-auto max-w-3xl px-6 pb-20">
      <h2 className="text-xl font-semibold mb-1">
        A simple deploy workflow
      </h2>
      <p className="text-sm text-gray-500 mb-4">
        Push to main. That&rsquo;s it.
      </p>

      <div className="rounded-lg bg-[#111] text-sm font-[JetBrains_Mono] overflow-hidden">
        <div className="px-4 py-2.5 border-b border-white/10 text-gray-500 text-xs">
          ~/myapp
        </div>
        <div className="p-4 leading-relaxed text-gray-400">
          <div>
            <span className="text-gray-500">$</span>{" "}
            <span className="text-white">git push origin main</span>
          </div>
          <div className="mt-3">
            <span className="text-blue-400">kindling</span> · Building from
            commit <span className="text-white">a1b2c3d</span>
          </div>
          <div>
            <span className="text-blue-400">kindling</span> · Detected{" "}
            <span className="text-white">Next.js</span> — injecting optimized
            Dockerfile
          </div>
          <div>
            <span className="text-blue-400">kindling</span> · OCI image built
            in <span className="text-white">34s</span>
          </div>
          <div>
            <span className="text-blue-400">kindling</span> · MicroVM booted
            in <span className="text-white">280ms</span>
          </div>
          <div>
            <span className="text-blue-400">kindling</span> · TLS provisioned
            via CertMagic
          </div>
          <div className="mt-3 text-green-400">
            ✓ Live at https://myapp.example.com
          </div>
        </div>
      </div>

      <h2 className="text-xl font-semibold mt-12 mb-1">
        ...or use the CLI
      </h2>
      <p className="text-sm text-gray-500 mb-4">
        Create projects and trigger deploys from your terminal.
      </p>

      <div className="rounded-lg bg-[#111] text-sm font-[JetBrains_Mono] overflow-hidden">
        <div className="px-4 py-2.5 border-b border-white/10 text-gray-500 text-xs">
          ~/
        </div>
        <div className="p-4 leading-relaxed text-gray-400">
          <div className="text-gray-500">
            # Create a project linked to your GitHub repo
          </div>
          <div>
            <span className="text-gray-500">$</span>{" "}
            <span className="text-white">
              kindling project create --name myapp --repo
              github.com/user/myapp
            </span>
          </div>
          <div className="mt-3 text-gray-500">
            # Trigger a deploy from a specific commit or branch
          </div>
          <div>
            <span className="text-gray-500">$</span>{" "}
            <span className="text-white">
              kindling deploy trigger --project myapp
            </span>
          </div>
        </div>
      </div>
    </section>
  )
}
