import { Check, X } from "lucide-react"

function CellValue({ value }: { value: string | boolean }) {
  if (value === true) return <Check className="w-4 h-4 text-black" />
  if (value === false) return <X className="w-4 h-4 text-gray-300" />
  return <span>{value}</span>
}

export function Comparison() {
  return (
    <section id="comparison" className="mx-auto max-w-3xl px-6 py-16 border-t border-gray-200">
      <h2 className="text-2xl font-bold tracking-tight">
        Why microVMs over containers?
      </h2>
      <p className="mt-2 text-gray-500 text-sm mb-8">
        Cloud Hypervisor uses the same rust-vmm crates as AWS Firecracker.
        Full hypervisor isolation with container-like speed.
      </p>

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-200 text-left">
              <th className="py-3 pr-4 font-medium text-gray-400 w-40" />
              <th className="py-3 px-4 font-medium text-gray-600">
                Containers
              </th>
              <th className="py-3 px-4 font-medium text-gray-600">
                Traditional VMs
              </th>
              <th className="py-3 px-4 font-semibold text-black">MicroVMs</th>
            </tr>
          </thead>
          <tbody className="text-gray-600">
            {[
              ["Boot time", "~1s", "30–60s", "<300ms"],
              ["Isolation", "Shared kernel", "Full", "Full (minimal surface)"],
              ["Memory overhead", "~10MB", "~200MB", "<5MB"],
              [
                "Security boundary",
                "Namespace",
                "Hypervisor",
                "Hypervisor (KVM)",
              ],
            ].map((row) => (
              <tr key={row[0]} className="border-b border-gray-100">
                <td className="py-3 pr-4 text-gray-400 font-medium">
                  {row[0]}
                </td>
                <td className="py-3 px-4">{row[1]}</td>
                <td className="py-3 px-4">{row[2]}</td>
                <td className="py-3 px-4 text-black font-medium">{row[3]}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h2 className="text-2xl font-bold tracking-tight mt-16">
        How Kindling compares
      </h2>
      <p className="mt-2 text-gray-500 text-sm mb-8">
        The only self-hosted PaaS with microVM isolation and declarative
        reconciliation.
      </p>

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-gray-200 text-left">
              <th className="py-3 pr-4 font-medium text-gray-400 w-40" />
              <th className="py-3 px-4 font-medium text-gray-600">Railway</th>
              <th className="py-3 px-4 font-medium text-gray-600">Render</th>
              <th className="py-3 px-4 font-medium text-gray-600">Coolify</th>
              <th className="py-3 px-4 font-semibold text-black">Kindling</th>
            </tr>
          </thead>
          <tbody className="text-gray-600">
            {(
              [
                {
                  label: "Self-hosted",
                  values: [false, false, true, true],
                },
                {
                  label: "Isolation",
                  values: ["Container", "Container", "Container", "MicroVM"],
                },
                {
                  label: "Scale-to-zero",
                  values: [false, "Paid tier", false, "<500ms"],
                },
                {
                  label: "Build isolation",
                  values: ["Shared", "Shared", "Shared", "Per-build VM"],
                },
                {
                  label: "State mgmt",
                  values: [
                    "Imperative",
                    "Imperative",
                    "Imperative",
                    "Reconciler",
                  ],
                },
                {
                  label: "Open source",
                  values: [false, false, true, true],
                },
              ] as { label: string; values: (string | boolean)[] }[]
            ).map((row) => (
              <tr key={row.label} className="border-b border-gray-100">
                <td className="py-3 pr-4 text-gray-400 font-medium">
                  {row.label}
                </td>
                {row.values.map((val, i) => (
                  <td
                    key={i}
                    className={`py-3 px-4 ${i === 3 ? "text-black font-medium" : ""}`}
                  >
                    <CellValue value={val} />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
