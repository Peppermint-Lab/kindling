# Environment

Environment variables, external dependencies, and setup notes.

**What belongs here:** Required env vars, external API keys/services, dependency quirks, platform-specific notes.
**What does NOT belong here:** Service ports/commands (use `.factory/services.yaml`).

---

## Go Toolchain

- Go 1.26
- macOS development environment (Apple Silicon)
- No database required for tests — `go test ./...` passes without Postgres
- e2e tests require Postgres and are excluded by default (need `-tags=integration`)

## Build Quirks

- macOS linker warning `ignoring duplicate libraries: '-lobjc'` is harmless, ignore it
- `go build ./...` includes `cmd/guest-agent/` which has Linux-specific files — they compile on macOS via build tags
