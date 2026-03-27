export type SemVer = {
  major: number
  minor: number
  patch: number
  prerelease?: string
  build?: string
}

export type VersionBump = "major" | "minor" | "patch"

export type ConventionalCommit = {
  type: string
  breaking: boolean
  description: string
}

export type VersionSnapshot = SemVer & {
  normalized: string
  tag: string
}

export type AutoIncrementVersionInput = {
  baseVersion: string
  commitSubjects?: string[]
  shortSha?: string | null
  dirty?: boolean
  exactTag?: string | null
}

const DEFAULT_VERSION = "0.1.0"
const SEMVER_PATTERN =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/
const CONVENTIONAL_COMMIT_PATTERN =
  /^(?<type>[a-z]+)(?:\([^)]+\))?(?<breaking>!)?:\s*(?<description>.+)$/i
const RELEASE_TAG_PATTERN = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/

export function parseSemVer(value: string): SemVer | null {
  const match = SEMVER_PATTERN.exec(value.trim())
  if (!match) return null

  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] || undefined,
    build: match[5] || undefined,
  }
}

export function formatSemVer(version: SemVer): string {
  const base = `${version.major}.${version.minor}.${version.patch}`
  const prerelease = version.prerelease ? `-${version.prerelease}` : ""
  const build = version.build ? `+${version.build}` : ""
  return `${base}${prerelease}${build}`
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

export function highestVersionBump(subjects: string[]): VersionBump | null {
  let highest: VersionBump | null = null

  for (const subject of subjects) {
    const bump = versionBumpForCommit(subject)
    if (bump === "major") return "major"
    if (bump === "minor") highest = highest === "patch" || highest === null ? "minor" : highest
    if (bump === "patch" && highest === null) highest = "patch"
  }

  return highest
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

export function isReleaseTag(tag: string): boolean {
  return RELEASE_TAG_PATTERN.test(tag.trim())
}

export function versionFromReleaseTag(tag: string): string | null {
  return isReleaseTag(tag) ? tag.trim().slice(1) : null
}

export function resolveVersionSnapshot(value = DEFAULT_VERSION): VersionSnapshot {
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

export function resolveAutoIncrementVersion({
  baseVersion,
  commitSubjects = [],
  shortSha,
  dirty = false,
  exactTag,
}: AutoIncrementVersionInput): VersionSnapshot {
  const taggedVersion = exactTag ? versionFromReleaseTag(exactTag) : null
  if (taggedVersion) {
    const version = parseSemVer(taggedVersion) ?? parseSemVer(DEFAULT_VERSION)!
    if (!dirty) return toVersionSnapshot(version)
    return toVersionSnapshot({
      ...version,
      build: "dirty",
    })
  }

  const current = parseSemVer(baseVersion) ?? parseSemVer(DEFAULT_VERSION)!
  const highestBumpNeeded = highestVersionBump(commitSubjects)
  const commitCount = commitSubjects.length
  const nextRelease = highestBumpNeeded ? bumpSemVer(current, highestBumpNeeded) : current

  if (commitCount === 0 && !dirty) {
    return toVersionSnapshot(current)
  }

  const buildParts = [shortSha?.trim() || "", dirty ? "dirty" : ""].filter(Boolean)

  return toVersionSnapshot({
    ...nextRelease,
    prerelease: commitCount > 0 ? `dev.${commitCount}` : undefined,
    build: buildParts.length > 0 ? buildParts.join(".") : undefined,
  })
}
