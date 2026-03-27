const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

const MONTHS = [
  "Jan", "Feb", "Mar", "Apr", "May", "Jun",
  "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
] as const

export function timeAgo(dateString: string | null | undefined): string {
  if (!dateString) return ""
  const date = new Date(dateString)
  const now = Date.now()
  const diff = now - date.getTime()

  if (diff < 0) return "just now"
  if (diff < MINUTE) return `${Math.max(1, Math.floor(diff / SECOND))}s`
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)}m`
  if (diff < DAY) return `${Math.floor(diff / HOUR)}h`
  if (diff < 7 * DAY) return `${Math.floor(diff / DAY)}d`

  const month = MONTHS[date.getMonth()]
  const day = date.getDate()
  const currentYear = new Date().getFullYear()
  if (date.getFullYear() !== currentYear) {
    return `${month} ${day}, ${date.getFullYear()}`
  }
  return `${month} ${day}`
}
