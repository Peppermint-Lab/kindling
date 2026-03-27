import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

const args = new Map(
  process.argv.slice(2).map((arg) => {
    const [key, value = "true"] = arg.split("=")
    return [key, value]
  }),
)

const maxJsKb = Number(args.get("--max-js-kb") || "0")
const distAssetsDir = path.resolve("dist/assets")

function formatKiB(bytes) {
  return `${(bytes / 1024).toFixed(2)} KiB`
}

function packageKeyForSource(source) {
  const [, packagePath = ""] = source.split("node_modules/")
  const parts = packagePath.split("/")
  if (parts[0]?.startsWith("@")) {
    return parts.slice(0, 2).join("/")
  }
  return parts[0] || "(unknown)"
}

function topEntries(entries, limit = 10) {
  return [...entries.entries()].sort((a, b) => b[1] - a[1]).slice(0, limit)
}

function printTop(title, rows) {
  if (rows.length === 0) return
  console.log(`\n${title}`)
  for (const [name, size] of rows) {
    console.log(`- ${formatKiB(size)} ${name}`)
  }
}

const assetFiles = readdirSync(distAssetsDir)
const jsFiles = assetFiles
  .filter((file) => file.endsWith(".js"))
  .map((file) => ({
    file,
    path: path.join(distAssetsDir, file),
    size: statSync(path.join(distAssetsDir, file)).size,
  }))
  .sort((a, b) => b.size - a.size)

if (jsFiles.length === 0) {
  console.error("No JS assets found in dist/assets.")
  process.exit(1)
}

const largestJs = jsFiles[0]
console.log(`Largest JS chunk: ${largestJs.file} (${formatKiB(largestJs.size)})`)

if (jsFiles.length > 1) {
  console.log("\nAll JS chunks")
  for (const chunk of jsFiles) {
    console.log(`- ${chunk.file}: ${formatKiB(chunk.size)}`)
  }
}

const mapPath = `${largestJs.path}.map`
let topAppFiles = []
let topPackages = []

try {
  const sourceMap = JSON.parse(readFileSync(mapPath, "utf8"))
  const appTotals = new Map()
  const packageTotals = new Map()

  for (const [index, source] of sourceMap.sources.entries()) {
    const sourceContent = sourceMap.sourcesContent?.[index] || ""
    const length = sourceContent.length
    if (length === 0) continue

    if (source.includes("node_modules/")) {
      const key = packageKeyForSource(source)
      packageTotals.set(key, (packageTotals.get(key) || 0) + length)
      continue
    }

    const srcIndex = source.indexOf("/src/")
    const key = srcIndex >= 0 ? source.slice(srcIndex + 1) : source.replace(/^\.\.\/\.\.\//, "")
    appTotals.set(key, (appTotals.get(key) || 0) + length)
  }

  topAppFiles = topEntries(appTotals)
  topPackages = topEntries(packageTotals)
} catch {
  console.log("\nNo sourcemap found for the largest chunk; skipping source breakdown.")
}

printTop("Top app sources (by sourcemap source length)", topAppFiles)
printTop("Top packages (by sourcemap source length)", topPackages)

if (maxJsKb > 0) {
  const budgetBytes = maxJsKb * 1024
  if (largestJs.size > budgetBytes) {
    console.error(
      `\nLargest JS chunk exceeds budget: ${formatKiB(largestJs.size)} > ${formatKiB(budgetBytes)}.`,
    )
    process.exit(1)
  }

  console.log(`\nBundle budget passed: ${formatKiB(largestJs.size)} <= ${formatKiB(budgetBytes)}.`)
}
