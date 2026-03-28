# User Testing

Testing surface, required testing skills/tools, and resource cost classification.

## Validation Surface

This is a **refactoring mission** with no UI/API surface to test manually. All validation is automated.

**Primary surface:** Shell commands
- `go build ./...` — compilation gate
- `go vet ./...` — static analysis gate
- `go test ./...` — behavioral preservation gate
- `grep`/`wc`/`ls` — structural verification (file counts, pattern presence/absence)

**Tool:** Shell commands only. No agent-browser, tuistory, or curl needed.

## Validation Concurrency

**Surface: shell**
- Shell commands are lightweight (go build uses ~2GB RAM peak, tests use ~1GB)
- Machine: 16GB RAM, 10 CPU cores
- Max concurrent validators: **5** (each validator runs a few shell commands, minimal resource overlap)

## Flow Validator Guidance: shell

- **Isolation boundary:** repository root `/Users/jack/code/kindling` only.
- Do not modify source code or mission planning files while validating assertions.
- Write flow reports only to `.factory/validation/<milestone>/user-testing/flows/*.json`.
- Save evidence artifacts only under `{missionDir}/evidence/<milestone>/<group-id>/`.
- Avoid running `go build`, `go vet`, and `go test` concurrently with each other; run them serially in one validator group to reduce cache/contention noise.
- On macOS, prefer `sed` + `grep` (or `python`) over complex `awk` parsing for function-range extraction to avoid BSD `awk` portability pitfalls.
