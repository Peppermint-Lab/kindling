import { describe, expect, it } from "vitest"

import {
  bumpSemVer,
  nextVersionForCommit,
  parseConventionalCommit,
  parseSemVer,
  resolveVersionSnapshot,
  versionBumpForCommit,
} from "@/lib/version"

describe("version", () => {
  it("parses semantic versions", () => {
    expect(parseSemVer("0.1.0")).toEqual({ major: 0, minor: 1, patch: 0 })
    expect(parseSemVer("v0.1.0")).toBeNull()
  })

  it("resolves the dashboard version snapshot", () => {
    expect(resolveVersionSnapshot("0.1.0")).toMatchObject({
      major: 0,
      minor: 1,
      patch: 0,
      normalized: "0.1.0",
      tag: "v0.1.0",
    })
  })

  it("parses conventional commit subjects", () => {
    expect(parseConventionalCommit("feat(ui): add version badge")).toEqual({
      type: "feat",
      breaking: false,
      description: "add version badge",
    })
  })

  it("maps conventional commits to version bumps from the spec", () => {
    expect(versionBumpForCommit("feat: add dashboard versioning")).toBe("minor")
    expect(versionBumpForCommit("fix: correct sidebar alignment")).toBe("patch")
    expect(versionBumpForCommit("feat!: replace auth flow")).toBe("major")
    expect(versionBumpForCommit("docs: update README")).toBeNull()
  })

  it("bumps semantic versions", () => {
    expect(bumpSemVer({ major: 0, minor: 1, patch: 0 }, "minor")).toEqual({
      major: 0,
      minor: 2,
      patch: 0,
    })
  })

  it("computes the next tagged version for a commit", () => {
    expect(nextVersionForCommit("0.1.0", "fix: patch login redirect")?.tag).toBe(
      "v0.1.1"
    )
  })
})
