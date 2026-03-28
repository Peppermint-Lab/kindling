import { CopyIcon, ExternalLinkIcon, GlobeIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import type { DeploymentReachability as DeploymentReachabilityData } from "@/lib/api"
import { isPrimaryPublicURLRedirect } from "@/lib/deployment-reachability"

async function copyText(label: string, text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    console.warn("clipboard failed", label)
  }
}

type DeploymentReachabilityProps = {
  reachable?: DeploymentReachabilityData | null
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

export function DeploymentReachability({ reachable }: DeploymentReachabilityProps) {
  if (!reachable) {
    return <p className="text-sm text-muted-foreground">Not reachable yet.</p>
  }

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
        </div>
      ) : null}
    </div>
  )
}
