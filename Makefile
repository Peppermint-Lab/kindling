.PHONY: build dev db db-down migrate sqlc vet clean e2e-drain \
       landing-dev landing-build \
       install-deps remote-provision remote-sync remote-build remote-initramfs remote-run \
       dev-up dev-down dashboard-build remote-dashboard remote-restart

DATABASE_URL ?= postgres://kindling:kindling@localhost:5432/kindling?sslmode=disable
# Empty database recommended (e.g. `createdb kindling_e2e` once). See `contrib/e2e-drain.sh`.
E2E_DATABASE_URL ?= postgres://kindling:kindling@127.0.0.1:5432/kindling_e2e?sslmode=disable
REMOTE_HOST ?= kindling-dev
REMOTE_DIR ?= /home/ubuntu/kindling
# Optional: OVH public IPv4 so runtime_url uses it (passed to kindling serve --advertise-host)
REMOTE_PUBLIC_IP ?=
# Optional: public IP or DNS of the host so API/dashboard show a browser-openable runtime_url (crun/cloud-hypervisor publish 0.0.0.0:port).
KINDLING_RUNTIME_ADVERTISE_HOST ?=

# Vite injects this as the API origin for production static dashboard builds (must match public_base_url on split-host).
# Override for local builds, e.g. DASHBOARD_API_URL=http://127.0.0.1:8080 make dashboard-build
DASHBOARD_API_URL ?= https://api.kindling.systems

# === Local ===

# Build the binary (codesign with entitlements on macOS for Apple VZ)
build:
	go build -o bin/kindling ./cmd/kindling
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign --entitlements contrib/kindling.entitlements --force -s - bin/kindling; \
	fi

# Run locally (requires Postgres via `make db`)
dev: build kernel initramfs
	./bin/kindling serve

# Data directory
KINDLING_DATA ?= $(HOME)/.kindling

# Download prebuilt kernel (or build from source with `make kernel-build`)
KERNEL_RELEASE ?= kernel-v0.1.0
kernel:
	@mkdir -p $(KINDLING_DATA)
	@test -f $(KINDLING_DATA)/vmlinuz.bin && exit 0 || true
	@ARCH=$$(uname -m); \
	if [ "$$ARCH" = "arm64" ]; then ARCH="arm64"; fi; \
	echo "Downloading Kindling kernel ($$ARCH)..." && \
	curl -fsSL "https://github.com/Peppermint-Lab/kindling/releases/download/$(KERNEL_RELEASE)/vmlinuz-$$ARCH" \
		-o $(KINDLING_DATA)/vmlinuz.bin && \
	echo "Kernel downloaded to $(KINDLING_DATA)/vmlinuz.bin" || \
	(echo "Prebuilt kernel not found. Building from source..." && bash scripts/build-kernel.sh)

# Download prebuilt initramfs
initramfs-download:
	@mkdir -p $(KINDLING_DATA)
	@test -f $(KINDLING_DATA)/initramfs.cpio.gz && exit 0 || true
	@ARCH=$$(uname -m); \
	if [ "$$ARCH" = "arm64" ]; then ARCH="arm64"; fi; \
	echo "Downloading initramfs ($$ARCH)..." && \
	curl -fsSL "https://github.com/Peppermint-Lab/kindling/releases/download/$(KERNEL_RELEASE)/initramfs-$$ARCH.cpio.gz" \
		-o $(KINDLING_DATA)/initramfs.cpio.gz && \
	echo "Initramfs downloaded to $(KINDLING_DATA)/initramfs.cpio.gz"

# Build kernel from source (only needed to create new releases)
kernel-build:
	@rm -f $(KINDLING_DATA)/vmlinuz.bin
	@bash scripts/build-kernel.sh

# Build initramfs with guest agent (requires GNU cpio; see scripts/build-initramfs-local.sh)
initramfs:
	@bash scripts/build-initramfs-local.sh

# Start local Postgres (see contrib/dev-postgres.sh; no Docker required)
db:
	@bash contrib/dev-postgres.sh start

# Stop local Postgres
db-down:
	@bash contrib/dev-postgres.sh stop

# Run schema migration
migrate:
	psql $(DATABASE_URL) -f internal/database/schema.sql

# Regenerate sqlc
sqlc:
	sqlc generate

# Lint
vet:
	go vet ./...

# End-to-end drain API + server reconciler (Postgres required; not run in default `go test ./...`).
e2e-drain:
	KINDLING_E2E_DATABASE_URL="$(E2E_DATABASE_URL)" go test -tags=integration -v ./internal/e2e/... -count=1

# Clean build artifacts
clean:
	rm -rf bin/

# === Landing Page ===

landing-dev:
	@cd web/landing && npx vite --port 5174

landing-build:
	@cd web/landing && npm run build

# === Dashboard (Vite → web/dashboard/dist) ===

dashboard-build:
	@cd web/dashboard && VITE_API_URL="$(DASHBOARD_API_URL)" npm run build

# Build with DASHBOARD_API_URL and sync dist/ to REMOTE_HOST (remote-sync excludes dist/)
remote-dashboard: dashboard-build
	rsync -az web/dashboard/dist/ $(REMOTE_HOST):$(REMOTE_DIR)/web/dashboard/dist/

remote-restart:
	ssh $(REMOTE_HOST) 'sudo systemctl restart kindling'

# === Remote Dev (OVH) ===

# Local macOS/Linux dev: Homebrew (Darwin) or prints sudo command (Linux) — see contrib/install-host-deps.sh
install-deps:
	@bash contrib/install-host-deps.sh

# One-time: install host deps on remote, then provision Go/firmware/kernel
remote-provision:
	scp contrib/install-host-deps.sh $(REMOTE_HOST):/tmp/kindling-install-host-deps.sh
	ssh $(REMOTE_HOST) 'sudo bash /tmp/kindling-install-host-deps.sh --all'
	ssh $(REMOTE_HOST) 'bash -s' < contrib/remote/provision.sh

# Sync code to remote
remote-sync:
	rsync -az --delete \
		--exclude '.git' \
		--exclude 'node_modules' \
		--exclude 'bin' \
		--exclude 'dist' \
		--exclude '.claude' \
		. $(REMOTE_HOST):$(REMOTE_DIR)/

# Build on remote
remote-build: remote-sync
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && go build -o bin/kindling ./cmd/kindling && \
		sudo setcap cap_net_admin,cap_net_bind_service+ep "$(REMOTE_DIR)/bin/kindling" && \
		(if [ -f /usr/local/bin/cloud-hypervisor ]; then sudo setcap cap_net_admin+ep /usr/local/bin/cloud-hypervisor; fi)'

# Build initramfs on remote
remote-initramfs: remote-sync
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && bash scripts/build-initramfs.sh'

# Set up VM networking on remote
remote-networking:
	ssh $(REMOTE_HOST) 'bash -s' < scripts/setup-networking.sh

# Run kindling on remote
remote-run: remote-build
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && \
		if [ -n "$(REMOTE_PUBLIC_IP)" ]; then \
			./bin/kindling serve --advertise-host "$(REMOTE_PUBLIC_IP)"; \
		else \
			./bin/kindling serve; \
		fi'

# Install systemd + /etc/kindling on REMOTE_HOST (run once per server; needs sudo)
remote-prod-install: remote-build
	ssh $(REMOTE_HOST) 'sudo bash $(REMOTE_DIR)/contrib/setup-kindling-prod.sh'

# Full dev setup: sync, build, run API + dashboard
dev-up: remote-build
	@echo "Stopping any existing kindling..."
	-@ssh $(REMOTE_HOST) 'pkill -f "bin/kindling" 2>/dev/null; true'
	-@pkill -f "ssh -f -N -L 8080:localhost:8080" 2>/dev/null; true
	@sleep 1
	@echo "Starting SSH tunnel..."
	@ssh -f -N -L 8080:localhost:8080 $(REMOTE_HOST)
	@echo "Starting kindling API on $(REMOTE_HOST)..."
	@ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && nohup ./bin/kindling serve > /tmp/kindling.log 2>&1 &'
	@sleep 2
	@echo "Starting dashboard..."
	@cd web/dashboard && nohup npx vite > /tmp/kindling-dashboard.log 2>&1 &
	@echo ""
	@echo "=== Kindling dev environment ==="
	@echo "API:       http://localhost:8080"
	@echo "Dashboard: http://localhost:5173"
	@echo "Logs:      ssh $(REMOTE_HOST) tail -f /tmp/kindling.log"

# Stop everything
dev-down:
	-@ssh $(REMOTE_HOST) 'pkill -f "bin/kindling" 2>/dev/null; true'
	-@pkill -f "ssh -f -N -L 8080:localhost:8080" 2>/dev/null; true
	-@pkill -f "vite" 2>/dev/null; true
	@echo "Kindling stopped."
