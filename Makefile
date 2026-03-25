.PHONY: build dev db db-down migrate sqlc vet clean

DATABASE_URL ?= postgres://kindling:kindling@localhost:5432/kindling?sslmode=disable

# Build the binary
build:
	go build -o bin/kindling ./cmd/kindling

# Run locally (requires Postgres via `make db`)
dev: build
	DATABASE_URL=$(DATABASE_URL) ./bin/kindling serve

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
