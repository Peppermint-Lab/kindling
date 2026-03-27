import { AnsiUp } from "ansi_up"

/**
 * Build log data path (control plane → dashboard):
 *
 * 1. `internal/builder` writes one `build_logs` row per line while OCI builds run.
 * 2. `GET /api/deployments/:id/logs` returns all rows; `GET .../stream` (SSE) polls new rows and
 *    sends `logs` events (see `internal/rpc/api.go`, `internal/rpc/sse.go`).
 * 3. Dashboard merges chunks in `DeploymentDetailPage` and renders each `BuildLog.message` here.
 *
 * Messages are stored as plain `TEXT` (ANSI escapes preserved when tools emit them).
 *
 * **Source note:** Build tools often disable color without a TTY (`buildah` via `exec` has no PTY).
 * Logs may stay plain until the builder sets force-color env (e.g. `FORCE_COLOR=1`) or similar.
 * This module only affects presentation when escapes are present.
 */
export function buildLogAnsiToHtml(message: string): string {
  const converter = new AnsiUp()
  // Inline styles keep colors readable in both light and dark themes without defining CSS classes.
  converter.use_classes = false
  converter.escape_html = true
  return converter.ansi_to_html(message)
}
