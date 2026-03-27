import { describe, expect, it } from "vitest"

import { buildLogAnsiToHtml } from "./build-log-ansi"

describe("buildLogAnsiToHtml", () => {
  it("leaves plain text escaped and unchanged visually", () => {
    expect(buildLogAnsiToHtml("hello world")).toBe("hello world")
    expect(buildLogAnsiToHtml("<script>alert(1)</script>")).toBe(
      "&lt;script&gt;alert(1)&lt;/script&gt;",
    )
  })

  it("renders basic foreground ANSI color", () => {
    const red = "\x1b[31merror\x1b[0m"
    const html = buildLogAnsiToHtml(red)
    expect(html).toContain("error")
    expect(html).toContain("color:rgb(187,0,0)")
    expect(html).not.toContain("\x1b")
  })

  it("handles bold + reset", () => {
    const s = "\x1b[1mbold\x1b[0m normal"
    const html = buildLogAnsiToHtml(s)
    expect(html).toContain("font-weight:bold")
    expect(html).toContain("normal")
  })

  it("treats each line independently (fresh converter)", () => {
    const a = buildLogAnsiToHtml("\x1b[32mgreen\x1b[0m")
    const b = buildLogAnsiToHtml("no-color")
    expect(a).toContain("green")
    expect(b).toBe("no-color")
  })
})
