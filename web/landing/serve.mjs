import { createServer } from "node:http"
import { createReadStream, existsSync, statSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const rootDir = path.dirname(fileURLToPath(import.meta.url))
const distDir = path.join(rootDir, "dist")
const indexPath = path.join(distDir, "index.html")
const port = Number(process.env.PORT || "3000")

const contentTypes = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".ico": "image/x-icon",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".txt": "text/plain; charset=utf-8",
  ".webp": "image/webp",
}

function safeJoinDist(urlPath) {
  const pathname = decodeURIComponent(urlPath.split("?")[0] || "/")
  const normalized = path.normalize(pathname).replace(/^(\.\.(\/|\\|$))+/, "")
  const candidate = path.join(distDir, normalized)
  const relative = path.relative(distDir, candidate)
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    return null
  }
  return candidate
}

function sendFile(res, filePath, method) {
  const ext = path.extname(filePath).toLowerCase()
  res.statusCode = 200
  res.setHeader("Content-Type", contentTypes[ext] || "application/octet-stream")
  if (method === "HEAD") {
    res.end()
    return
  }
  createReadStream(filePath).pipe(res)
}

createServer((req, res) => {
  if (req.method !== "GET" && req.method !== "HEAD") {
    res.statusCode = 405
    res.setHeader("Allow", "GET, HEAD")
    res.end("Method Not Allowed")
    return
  }

  let filePath = safeJoinDist(req.url || "/")
  if (!filePath) {
    res.statusCode = 400
    res.end("Bad Request")
    return
  }

  if (existsSync(filePath) && statSync(filePath).isDirectory()) {
    filePath = path.join(filePath, "index.html")
  }

  if (!existsSync(filePath) || !statSync(filePath).isFile()) {
    filePath = indexPath
  }

  sendFile(res, filePath, req.method)
}).listen(port, "0.0.0.0", () => {
  console.log(`landing server listening on http://0.0.0.0:${port}`)
})
