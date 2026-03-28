import { CopyIcon, ExternalLinkIcon, GlobeIcon, NetworkIcon, RouteIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import type { DeploymentReachability as DeploymentReachabilityData } from "@/lib/api"
import { countAdditionalPublicEndpoints, isPrimaryPublicURLRedirect } from "@/lib/deployment-reachability"

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    console.warn("clipboard failed", label)
  }
}

type DeploymentReachabilityProps = {
  reachable?: DeploymentReachabilityData | null
  compact?: boolean
  showOperatorDetails?: boolean
}

function ReachabilityRow({
  label,
  value,
  href,
  copyLabel,
  description,
}: {
  label: string
  value: string
  href?: string
  copyLabel: string
  description?: string
}) {
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">{label}</p>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <code className="flex-1 rounded-md border bg-muted/50 px-3 py-2 text-xs font-mono break-all">{value}</code>
        <div className="flex gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => void copyText(copyLabel, value)}>
            <CopyIcon className="mr-2 size-3" />
            Copy
          </Button>
          {href ? (
            <a
              href={href}
              target="_blank"
              rel="noreferrer"
              className="inline-flex h-7 items-center rounded-[min(var(--radius-md),12px)] border border-border bg-background px-2.5 text-[0.8rem] font-medium hover:bg-muted"
            >
              <ExternalLinkIcon className="mr-2 size-3.5" />
              Open
            </a>
          ) : null}
        </div>
      </div>
      {description ? <p className="text-xs text-muted-foreground">{description}</p> : null}
    </div>
  )
}

export function DeploymentReachability({
  reachable,
  compact = false,
  showOperatorDetails = false,
}: DeploymentReachabilityProps) {
  if (!reachable) {
    return <p className="text-sm text-muted-foreground">Not reachable yet.</p>
  }

  const additionalEndpoints = reachable.public_endpoints?.slice(1) ?? []
  const additionalCount = countAdditionalPublicEndpoints(reachable)

  return (
    <div className="space-y-4 text-sm">
      {reachable.public_url ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <GlobeIcon className="size-4 text-muted-foreground" />
            Public URL
          </div>
          <ReachabilityRow
            label="Primary endpoint"
            value={reachable.public_url}
            href={reachable.public_url}
            copyLabel="public-url"
            description={
              isPrimaryPublicURLRedirect(reachable)
                ? "This hostname redirects before reaching the app."
                : "This hostname routes directly to the deployment."
            }
          />
          {isPrimaryPublicURLRedirect(reachable) && reachable.public_endpoints?.[0]?.redirect_to ? (
            <ReachabilityRow
              label="Redirect destination"
              value={reachable.public_endpoints[0].redirect_to}
              href={reachable.public_endpoints[0].redirect_to}
              copyLabel="redirect-destination"
              description={
                reachable.public_endpoints[0].redirect_status_code
                  ? `HTTP ${reachable.public_endpoints[0].redirect_status_code} redirect`
                  : undefined
              }
            />
          ) : null}
          {compact && additionalCount > 0 ? (
            <p className="text-xs text-muted-foreground">
              +{additionalCount} more public endpoint{additionalCount === 1 ? "" : "s"}
            </p>
          ) : null}
        </div>
      ) : null}

      {reachable.private_endpoints && reachable.private_endpoints.length > 0 ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <RouteIcon className="size-4 text-muted-foreground" />
            Private endpoints
          </div>
          <div className="space-y-3">
            {reachable.private_endpoints.map((endpoint) => (
              <ReachabilityRow
                key={`${endpoint.name}-${endpoint.dns_name}`}
                label={`${endpoint.name} (${endpoint.visibility})`}
                value={endpoint.dns_name}
                copyLabel={`private-${endpoint.name}`}
                description={`${endpoint.protocol.toUpperCase()} on port ${endpoint.port} via ${endpoint.private_ip}`}
              />
            ))}
          </div>
        </div>
      ) : null}

      {showOperatorDetails && reachable.runtime_url ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <NetworkIcon className="size-4 text-muted-foreground" />
            Operator details
          </div>
          <ReachabilityRow
            label="Raw runtime address"
            value={reachable.runtime_url}
            href={reachable.runtime_url}
            copyLabel="runtime-url"
            description="Host-level runtime endpoint for this deployment."
          />
          {reachable.vm_ip ? (
            <ReachabilityRow
              label="VM IP"
              value={reachable.vm_ip}
              copyLabel="vm-ip"
              description="Underlying backend IP recorded for this deployment."
            />
          ) : null}
        </div>
      ) : null}

      {!compact && additionalEndpoints.length > 0 ? (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm font-medium">
            <RouteIcon className="size-4 text-muted-foreground" />
            Additional public endpoints
          </div>
          <div className="space-y-3">
            {additionalEndpoints.map((endpoint) => (
              <ReachabilityRow
                key={endpoint.domain}
                label={endpoint.domain}
                value={endpoint.public_url}
                href={endpoint.public_url}
                copyLabel={endpoint.domain}
                description={
                  endpoint.proxies_to_deployment === false
                    ? endpoint.redirect_to
                      ? `Redirects to ${endpoint.redirect_to}${
                          endpoint.redirect_status_code ? ` (HTTP ${endpoint.redirect_status_code})` : ""
                        }`
                      : "Redirecting entry point"
                    : "Routes directly to the deployment."
                }
              />
            ))}
          </div>
        </div>
      ) : null}
    </div>
  )
}
