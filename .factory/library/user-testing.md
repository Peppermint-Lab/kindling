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
