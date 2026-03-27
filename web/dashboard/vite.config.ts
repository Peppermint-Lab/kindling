import { execSync } from "node:child_process"
import { readFileSync } from "node:fs"
import path from "path"
import { defineConfig } from "vitest/config"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import {
  isReleaseTag,
  resolveAutoIncrementVersion,
  versionFromReleaseTag,
} from "./src/lib/version-core"

const packageJson = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.url), "utf8")
) as { version: string }
const appVersion = resolveDashboardVersion(packageJson.version)

function runGit(command: string): string {
  try {
    return execSync(command, {
      cwd: __dirname,
      stdio: ["ignore", "pipe", "ignore"],
    })
      .toString()
      .trim()
  } catch {
    return ""
  }
}

function releaseTagsFromOutput(output: string): string[] {
  return output
    .split("\n")
    .map((tag) => tag.trim())
    .filter((tag) => tag.length > 0 && isReleaseTag(tag))
}

function resolveDashboardVersion(baseVersion: string): string {
  const exactTag = releaseTagsFromOutput(runGit("git tag --points-at HEAD"))[0] ?? null
  const latestMergedTag =
    releaseTagsFromOutput(runGit("git tag --merged HEAD --sort=-v:refname"))[0] ?? null
  const commitRange = latestMergedTag ? `${latestMergedTag}..HEAD` : ""
  const commitSubjects = runGit(
    commitRange ? `git log --format=%s ${commitRange}` : "git log --format=%s"
  )
    .split("\n")
    .map((subject) => subject.trim())
    .filter(Boolean)
  const shortSha = runGit("git rev-parse --short=7 HEAD") || null
  const dirty = runGit("git status --short").length > 0

  return resolveAutoIncrementVersion({
    baseVersion: versionFromReleaseTag(latestMergedTag ?? "") ?? baseVersion,
    commitSubjects,
    shortSha,
    dirty,
    exactTag,
  }).normalized
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    globals: false,
  },
})
