# Getting Started On Linux

This guide is the supported first path for running Kindling on a real host: one Linux machine, one PostgreSQL instance, one Kindling server.

## Prerequisites

- Linux host with shell access
- Go `1.26.x` available for building from source
- PostgreSQL available locally or reachable over the network
- Root or `sudo` access if you need to install host dependencies

Recommended:

- KVM support (`/dev/kvm`) so workloads run in Cloud Hypervisor microVMs
- Public DNS name or public IP if you want browser-openable runtime URLs or public API endpoints

Without KVM, Kindling still runs and automatically falls back to the `crun` runtime.

## Host Dependencies

The fastest repo-supported setup is:

```bash
make install-deps
```

That uses `contrib/install-host-deps.sh` to install the expected host tools for this machine.

For a fuller Linux server setup, including the broader server-side dependency set, run:

```bash
sudo ./contrib/install-host-deps.sh --all
```

Typical host tools include:

- `buildah`
- `skopeo`
- `umoci`
- `crun`
- `virt-make-fs`
- `cloud-hypervisor` on KVM-capable Linux hosts

## PostgreSQL Setup

For local development or a quick local server bring-up, you can use the repo helper:

```bash
make db
```

If you already have PostgreSQL running elsewhere, point Kindling at it using one of these DSN sources, in this order:

1. `/etc/kindling/postgres.dsn`
2. `~/.kindling/postgres.dsn`
3. Built-in fallback: `postgres://kindling:kindling@127.0.0.1:5432/kindling?sslmode=disable`

If you are bringing up a fresh database manually, run the schema migration once:

```bash
make migrate
```

## Build And Run

Build the binary:

```bash
make build
```

Start the server:

```bash
bin/kindling serve
```

At this point, the API listens on `:8080` by default. The server will auto-detect the best runtime for the host.

## First-Boot Flags

The most useful first-boot flags are:

- `--public-url` to seed `cluster_settings.public_base_url`
- `--advertise-host` to seed `server_settings.advertise_host` when you want runtime URLs to point at a public IP or DNS name
- `--dashboard-host` to seed a dedicated dashboard hostname when you split API and dashboard hosts

Examples:

```bash
bin/kindling serve --public-url https://kindling.example.com
bin/kindling serve --advertise-host 203.0.113.10
```

You can combine them on first boot if needed.

## Common Commands

```bash
make install-deps
make db
make build
make migrate
bin/kindling serve --help
go run ./cmd/kindling --help
```

## Troubleshooting

### No KVM Available

If `/dev/kvm` is missing or unavailable, Kindling does not stop. It falls back to `crun`. That is expected behavior, but you will not get Cloud Hypervisor microVM isolation on that host.

### Missing Build Tools

If builds fail early, confirm the required host tools are installed and on `PATH`, especially `buildah`, `skopeo`, `umoci`, and `crun`. On KVM-backed Linux hosts, `cloud-hypervisor` and `virt-make-fs` are also important for the microVM path.

### Database Connection Errors

If `kindling serve` cannot connect to PostgreSQL:

- verify which DSN source Kindling is using
- check that the database exists and accepts connections
- confirm the user/password in the DSN are correct
- run `make migrate` if the schema has not been applied yet

### Host Reachability And Runtime URLs

If runtime URLs are not browser-openable from another machine, seed `--advertise-host` with the public IP or DNS name the host should advertise, and make sure inbound firewall rules allow the required traffic.

## Next Steps

- Return to the [root README](../README.md)
- Read [Architecture](architecture.md) for the control-plane model
- Read [Development](development.md) if you are contributing inside the repo
