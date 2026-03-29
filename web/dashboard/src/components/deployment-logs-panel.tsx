import { useEffect, useRef } from "react"
import type { BuildLog } from "@/lib/api"
import { BuildLogLineBody } from "@/components/build-log-line-body"
import {
  Surface,
  SurfaceBody,
  SurfaceHeader,
  SurfaceTitle,
} from "@/components/page-surface"
import { LoaderIcon, ScrollTextIcon } from "lucide-react"

function logKey(log: BuildLog): string {
  if (log.id) return log.id
  return `${log.created_at}-${log.message.slice(0, 48)}`
}

export function DeploymentLogsPanel({
  logs,
  terminal,
}: {
  logs: BuildLog[]
  terminal: boolean
}) {
  const logEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [logs])

  return (
    <Surface className="border-white/[0.06]">
      <SurfaceHeader>
        <div className="flex items-center gap-2">
          <ScrollTextIcon className="size-4 text-muted-foreground" />
          <SurfaceTitle>Build logs</SurfaceTitle>
        </div>
      </SurfaceHeader>
      <SurfaceBody className="p-0">
        {logs.length === 0 ? (
          <div className="py-6 text-center">
            {!terminal ? (
              <div className="flex items-center justify-center gap-2 text-sm text-muted-foreground px-2">
                <LoaderIcon className="size-4 animate-spin shrink-0" />
                Waiting for build to start…
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">No build logs.</p>
            )}
          </div>
        ) : (
          <div className="rounded-lg bg-black/60 border border-white/[0.06] p-3.5 sm:p-4 font-mono text-xs leading-relaxed max-h-[min(70vh,720px)] overflow-y-auto space-y-0.5">
            {logs.map((log) => (
              <div
                key={logKey(log)}
                className={log.level === "error" ? "text-destructive" : "text-foreground"}
              >
                <span className="text-muted-foreground mr-2">
                  {log.created_at ? new Date(log.created_at).toLocaleTimeString() : ""}
                </span>
                <BuildLogLineBody message={log.message} />
              </div>
            ))}
            <div ref={logEndRef} />
          </div>
        )}
      </SurfaceBody>
    </Surface>
  )
}
