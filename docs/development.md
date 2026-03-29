# Development

This page covers the contributor workflow for building, running, and iterating on Kindling from the repo.

## Core Commands

```bash
make build
make db
make dev
make dashboard-build
make dev-up
make dev-down
```

What they do:

- `make build` builds `bin/kindling`
- `make db` starts the local PostgreSQL helper from `contrib/dev-postgres.sh`
- `make dev` builds the binary, prepares VM assets, and runs `kindling serve`
- `make dashboard-build` builds the dashboard SPA into `web/dashboard/dist`
- `make dev-up` syncs to the configured remote host, builds there, opens an SSH tunnel, starts the API remotely, and starts the local dashboard dev server
- `make dev-down` stops the remote API, tunnel, and local Vite process

## Local Development

For the common local loop:

```bash
make db
make build
bin/kindling serve
```

If you want the full local VM-backed development path instead of a manual server start:

```bash
make dev
```

Useful helpers:

- `make migrate` applies the schema in `internal/database/schema.sql`
- `make sqlc` regenerates sqlc code after query changes
- `make vet` runs `go vet ./...`
- `make e2e-drain` runs the drain integration suite against the configured E2E database

## Remote Development

The repo includes a remote development path aimed at a Linux host such as the OVH box referenced in the project notes.

Common flow:

```bash
make remote-provision
make dev-up
make dev-down
```

Related targets:

- `make remote-sync` copies the repo to `REMOTE_HOST`
- `make remote-build` builds `bin/kindling` on the remote host and applies required capabilities
- `make remote-initramfs` builds the initramfs remotely
- `make remote-run` starts the server on the remote host
- `make remote-dashboard` syncs built dashboard assets to the remote box
- `make remote-restart` restarts the systemd service on the remote box

The main variables are:

- `REMOTE_HOST`
- `REMOTE_DIR`
- `REMOTE_PUBLIC_IP`
- `DASHBOARD_API_URL`

## macOS Notes

macOS development uses Apple Virtualization rather than Cloud Hypervisor.

Important caveats:

- OCI image builds on macOS run inside a persistent Linux builder VM, not on the macOS host
- The builder VM expects a rootfs at `~/.kindling/builder-rootfs` containing `buildah`
- After changing `cmd/guest-agent`, rebuild the guest initramfs so builder mode is included
- `make initramfs` requires GNU `cpio`; on macOS that usually means installing it separately, for example via Homebrew
- Registry username/password are required for macOS builds so the guest can push and later pull the image

Relevant targets:

```bash
make kernel
make initramfs
make initramfs-download
make kernel-build
```

## Quality Bar

Current repo expectations:

- `go vet ./...` should pass
- sqlc changes start in `internal/database/queries.sql`, followed by `sqlc generate`
- Documentation should stay aligned with the Makefile, CLI help, and the vault-backed project spec

## Related Docs

- [Getting Started on Linux](getting-started-linux.md)
- [Architecture](architecture.md)
- [Internal DNS Runtime](internal-dns-runtime.md)
- [Root README](../README.md)
