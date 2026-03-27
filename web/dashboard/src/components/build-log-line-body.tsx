import { buildLogAnsiToHtml } from "@/lib/build-log-ansi"

/** Renders one build log line body with ANSI sequences converted to HTML. */
export function BuildLogLineBody({ message }: { message: string }) {
  const html = buildLogAnsiToHtml(message)
  return (
    <span
      className="whitespace-pre-wrap break-words [word-break:break-word]"
      // ansi_up escapes non-ANSI text and only outputs styled spans for escape sequences.
      dangerouslySetInnerHTML={{ __html: html }}
    />
  )
}
