set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Build operator binaries from committed embedded UI assets. Node is not used.
build:
    #!/usr/bin/env bash
    set -euo pipefail
    data_home="${FACTORY_DATA_HOME:-${FACTORY_V2_DATA_HOME:-${HOME:?HOME or FACTORY_DATA_HOME is required}/.factory}}"
    build_directory="${FACTORY_BUILD_DIR:-${FACTORY_V2_BUILD_DIR:-$data_home/bin}}"
    mkdir -p "$build_directory"
    go build -o "$build_directory/factory-server" ./cmd/factory-server
    go build -o "$build_directory/factory-worker" ./cmd/factory-worker
    go build -o "$build_directory/factory-poller" ./cmd/factory-poller
    printf 'Factory binaries built in %s\n' "$build_directory"

# Start one control plane and worker. Pass a worker config path when needed.
run config="":
    @if [[ -n "{{config}}" ]]; then ./scripts/run-local.sh "{{config}}"; else ./scripts/run-local.sh; fi

# Poll configured issue queues continuously. GitHub queues require authenticated gh.
poll config="":
    @if [[ -n "{{config}}" ]]; then go run ./cmd/factory-poller -config "{{config}}"; else go run ./cmd/factory-poller; fi

# Run one issue-queue pass and exit. GitHub queues require authenticated gh.
poll-once config="":
    @if [[ -n "{{config}}" ]]; then go run ./cmd/factory-poller -config "{{config}}" -once; else go run ./cmd/factory-poller -once; fi

# Install pinned UI dependencies.
ui-install:
    cd web && npm ci

# Rebuild committed embedded UI assets. Pass 0 to reuse installed dependencies.
ui-build install="1":
    @if [[ "{{install}}" == "1" ]]; then cd web && npm ci; fi
    cd web && npm run build

# Run UI lint, type checks, and component tests.
ui-check:
    cd web && npm run lint
    cd web && npm run typecheck
    cd web && npm test

# Run browser tests against the real Go server.
test-browser:
    cd web && npm run test:browser

# Report Go files that need formatting.
format-check:
    @test -z "$(find cmd internal migrations web -path web/node_modules -prune -o -name '*.go' -exec gofmt -l {} +)"

# Run Go static analysis.
vet:
    go vet ./...

# Prove workers do not import control-plane implementation code.
boundary:
    @! go list -deps ./internal/worker | grep -qx 'github.com/owainlewis/factory/internal/controlplane'

# Run all Go tests.
test:
    go test -timeout 5m ./...

# Test the Node-free build and Just command surface.
test-tooling:
    ./scripts/test-build.sh

# Test local startup, readiness, and signal handling.
test-launcher:
    ./scripts/test-run-local.sh

# Run the normal local and CI checks, excluding the slower browser suite.
check: format-check vet boundary test ui-check test-tooling test-launcher
