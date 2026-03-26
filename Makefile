.PHONY: build dev db db-down migrate sqlc vet clean \
       remote-provision remote-sync remote-build remote-initramfs remote-run \
       dev-up dev-down

DATABASE_URL ?= postgres://kindling:kindling@localhost:5432/kindling?sslmode=disable
REMOTE_HOST ?= kindling-dev
REMOTE_DIR ?= /home/ubuntu/kindling
# Optional: public IP or DNS of the host so API/dashboard show a browser-openable runtime_url (docker/crun publish 0.0.0.0:port).
KINDLING_RUNTIME_ADVERTISE_HOST ?=

# === Local ===

# Build the binary (codesign with entitlements on macOS for Apple VZ)
build:
	go build -o bin/kindling ./cmd/kindling
	@if [ "$$(uname)" = "Darwin" ]; then \
		codesign --entitlements contrib/kindling.entitlements --force -s - bin/kindling; \
	fi

# Run locally (requires Postgres via `make db`)
dev: build kernel initramfs
	DATABASE_URL=$(DATABASE_URL) ./bin/kindling serve

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

# Build initramfs with guest agent (cross-compile for Linux)
initramfs:
	@mkdir -p $(KINDLING_DATA)
	@GOARCH_TARGET="amd64"; \
	if [ "$$(uname -m)" = "arm64" ]; then GOARCH_TARGET="arm64"; fi; \
	echo "Cross-compiling guest agent for Linux/$$GOARCH_TARGET..."; \
	CGO_ENABLED=0 GOOS=linux GOARCH=$$GOARCH_TARGET go build -o /tmp/kindling-init ./cmd/guest-agent
	@echo "Building initramfs via Docker..."
	@PLATFORM="linux/amd64"; \
	if [ "$$(uname -m)" = "arm64" ]; then PLATFORM="linux/arm64"; fi; \
	docker run --rm --platform $$PLATFORM -v /tmp/kindling-init:/init:ro -v $(KINDLING_DATA):/out alpine:3.21 sh -c '\
		apk add --no-cache cpio busybox-static && \
		mkdir -p /rootfs/bin /rootfs/sbin /rootfs/etc /rootfs/proc /rootfs/sys /rootfs/dev /rootfs/tmp /rootfs/app /rootfs/usr/bin /rootfs/usr/sbin && \
		cp /init /rootfs/init && chmod +x /rootfs/init && \
		cp /bin/busybox.static /rootfs/bin/busybox && chmod +x /rootfs/bin/busybox && \
		for cmd in sh ip ifconfig route ping cat ls mkdir mount umount; do \
			ln -sf busybox /rootfs/bin/$$cmd; \
		done && \
		echo "nameserver 8.8.8.8" > /rootfs/etc/resolv.conf && \
		cd /rootfs && find . | cpio -o -H newc 2>/dev/null | gzip > /out/initramfs.cpio.gz'
	@rm -f /tmp/kindling-init
	@echo "Initramfs built: $(KINDLING_DATA)/initramfs.cpio.gz"

# Start local Postgres
db:
	docker compose up -d postgres

# Stop local Postgres
db-down:
	docker compose down

# Run schema migration
migrate:
	psql $(DATABASE_URL) -f internal/database/schema.sql

# Regenerate sqlc
sqlc:
	sqlc generate

# Lint
vet:
	go vet ./...

# Clean build artifacts
clean:
	rm -rf bin/

# === Remote Dev (OVH) ===

# One-time: provision the remote server
remote-provision:
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
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && go build -o bin/kindling ./cmd/kindling'

# Build initramfs on remote
remote-initramfs: remote-sync
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && bash scripts/build-initramfs.sh'

# Set up VM networking on remote
remote-networking:
	ssh $(REMOTE_HOST) 'bash -s' < scripts/setup-networking.sh

# Run kindling on remote
remote-run: remote-build
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && \
		DATABASE_URL=$(DATABASE_URL) \
		$(if $(KINDLING_RUNTIME_ADVERTISE_HOST),KINDLING_RUNTIME_ADVERTISE_HOST=$(KINDLING_RUNTIME_ADVERTISE_HOST),) \
		./bin/kindling serve'

# Full dev setup: sync, build, run API + dashboard
dev-up: remote-build
	@echo "Stopping any existing kindling..."
	-@ssh $(REMOTE_HOST) 'pkill -f "bin/kindling" 2>/dev/null; true'
	-@pkill -f "ssh -f -N -L 8080:localhost:8080" 2>/dev/null; true
	@sleep 1
	@echo "Starting SSH tunnel..."
	@ssh -f -N -L 8080:localhost:8080 $(REMOTE_HOST)
	@echo "Starting kindling API on $(REMOTE_HOST)..."
	@ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && \
		DATABASE_URL=$(DATABASE_URL) \
		$(if $(KINDLING_RUNTIME_ADVERTISE_HOST),KINDLING_RUNTIME_ADVERTISE_HOST=$(KINDLING_RUNTIME_ADVERTISE_HOST),) \
		nohup ./bin/kindling serve > /tmp/kindling.log 2>&1 &'
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
