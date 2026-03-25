.PHONY: build dev db db-down migrate sqlc vet clean \
       remote-provision remote-sync remote-build remote-initramfs remote-run \
       dev-up dev-down

DATABASE_URL ?= postgres://kindling:kindling@localhost:5432/kindling?sslmode=disable
REMOTE_HOST ?= kindling-dev
REMOTE_DIR ?= /home/ubuntu/kindling

# === Local ===

# Build the binary
build:
	go build -o bin/kindling ./cmd/kindling

# Run locally (requires Postgres via `make db`)
dev: build kernel
	DATABASE_URL=$(DATABASE_URL) ./bin/kindling serve

# Download kernel for Apple VZ / Cloud Hypervisor (one-time)
KINDLING_DATA ?= $(HOME)/.kindling
kernel:
	@mkdir -p $(KINDLING_DATA)
	@test -f $(KINDLING_DATA)/vmlinuz.bin || (echo "Downloading kernel..." && \
		curl -fsSL "https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.4.2/hypervisor-fw" -o $(KINDLING_DATA)/vmlinuz.bin && \
		echo "Kernel downloaded to $(KINDLING_DATA)/vmlinuz.bin")

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
	ssh $(REMOTE_HOST) 'cd $(REMOTE_DIR) && DATABASE_URL=$(DATABASE_URL) ./bin/kindling serve'

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
