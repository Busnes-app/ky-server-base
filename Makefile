.PHONY: all build build-web test test-race test-postgres test-replicas test-web tidy-check lint smoke ci run clean docker-build notes-check audit-web

all: build-web build

build-web:
	@echo "==> Building frontend web assets..."
	@cd web && npm ci && npm run build

build:
	@echo "==> Compiling ky_server_base binary..."
	@go build -o ky_server_base ./cmd/server

test:
	@echo "==> Running test suite..."
	@go test -v ./...

test-race:
	@echo "==> Running test suite with race detector..."
	@go test -race -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

# Runs the same suite against PostgreSQL; needs a reachable server.
test-postgres:
	@echo "==> Running test suite against PostgreSQL..."
	@KY_TEST_POSTGRES_DSN="$${KY_TEST_POSTGRES_DSN:-postgres://postgres:postgrespassword@127.0.0.1:5432/ky_server?sslmode=disable}" go test -count=1 ./...

# Builds the image and runs the Compose replica failure tests (Docker; a few minutes). The
# test removes its own project; relays stay off in every throwaway container.
test-replicas:
	@python3 scripts/check-replicas-compose.py
	@docker build -t ky_server_base:local .
	@KY_E2E_COMPOSE=1 KY_IMAGE=ky_server_base:local PUSH_RELAY_URL= APNS_RELAY_URL= go test -count=1 -v -timeout 30m ./e2e

test-web:
	@echo "==> Running frontend test suite..."
	@cd web && npm ci && npm test

audit-web:
	@cd web && npm run audit

# The same gate CI runs: a stale go.sum fails the build there, so fail here first.
tidy-check:
	@echo "==> Checking go.mod is tidy..."
	@go mod tidy
	@git diff --exit-code go.mod go.sum

lint:
	@echo "==> Checking formatting and vet..."
	@test -z "$$(gofmt -l $$(git ls-files '*.go'))" || { echo "gofmt needed:"; gofmt -l $$(git ls-files '*.go'); exit 1; }
	@go vet ./...

smoke: build
	@python3 -B -m unittest discover -s scripts -p test_ky_init.py
	@./scripts/smoke-test.sh

notes-check:
	@python3 -B -m unittest discover -s scripts -p test_release_notes.py
	@python3 scripts/check-release-notes.py

ci: notes-check tidy-check lint test-race test-web audit-web smoke
	@echo "==> Local CI checks passed"

run: build
	@./ky_server_base

docker-build:
	@docker build -t ky_server_base .

clean:
	@rm -rf ky_server_base web/dist web/node_modules coverage.out
