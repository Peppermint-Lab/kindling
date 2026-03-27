export type SemVer = {
  major: number
  minor: number
  patch: number
}

export type VersionBump = "major" | "minor" | "patch"

export type ConventionalCommitType =
  | "feat"
  | "fix"
  | "docs"
  | "chore"
  | "refactor"
  | "test"
  | "perf"

export type ConventionalCommit = {
  type: string
  breaking: boolean
  description: string
}

export type VersionSnapshot = SemVer & {
  normalized: string
  tag: string
}

const DEFAULT_VERSION = "0.1.0"
const SEMVER_PATTERN =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/
const CONVENTIONAL_COMMIT_PATTERN =
  /^(?<type>[a-z]+)(?:\([^)]+\))?(?<breaking>!)?:\s*(?<description>.+)$/i

export function parseSemVer(value: string): SemVer | null {
  const match = SEMVER_PATTERN.exec(value.trim())
  if (!match) return null

  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
  }
}

export function formatSemVer(version: SemVer): string {
  return `${version.major}.${version.minor}.${version.patch}`
}

export function toVersionSnapshot(version: SemVer): VersionSnapshot {
  const normalized = formatSemVer(version)
  return {
    ...version,
    normalized,
    tag: `v${normalized}`,
  }
}

export function parseConventionalCommit(subject: string): ConventionalCommit | null {
  const match = CONVENTIONAL_COMMIT_PATTERN.exec(subject.trim())
  if (!match?.groups) return null

  return {
    type: match.groups.type.toLowerCase(),
    breaking: match.groups.breaking === "!",
    description: match.groups.description,
  }
}

export function versionBumpForCommit(subject: string): VersionBump | null {
  const commit = parseConventionalCommit(subject)
  if (!commit) return null
  if (commit.breaking && (commit.type === "feat" || commit.type === "fix")) {
    return "major"
  }
  if (commit.type === "feat") return "minor"
  if (commit.type === "fix") return "patch"
  return null
}

export function bumpSemVer(version: SemVer, bump: VersionBump): SemVer {
  if (bump === "major") {
    return { major: version.major + 1, minor: 0, patch: 0 }
  }
  if (bump === "minor") {
    return { major: version.major, minor: version.minor + 1, patch: 0 }
  }
  return { major: version.major, minor: version.minor, patch: version.patch + 1 }
}

export function resolveVersionSnapshot(value = __APP_VERSION__): VersionSnapshot {
  return toVersionSnapshot(parseSemVer(value) ?? parseSemVer(DEFAULT_VERSION)!)
}

export function nextVersionForCommit(
  currentVersion: string,
  subject: string
): VersionSnapshot | null {
  const current = parseSemVer(currentVersion)
  const bump = versionBumpForCommit(subject)
  if (!current || !bump) return null
  return toVersionSnapshot(bumpSemVer(current, bump))
}

export const kindlingVersion = resolveVersionSnapshot()
