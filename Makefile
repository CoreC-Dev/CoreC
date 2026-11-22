# CoreC Makefile — one-command gate reproduction
#
# Usage:
#   make gates       — run all gates (lint + vet + fix-check + test + arch + taste + docs + cover)
#   make build       — build the binary
#   make test        — run short tests with race detector
#   make lint        — run golangci-lint (auto-installs the pinned version)
#   make fix         — apply go fix modernizers to fixpoint
#   make fix-check   — fail when go fix still has fixes to apply
#   make arch        — run architecture structure tests
#   make taste       — run taste invariant tests
#   make docs        — run document lint
#   make cover       — generate coverage report
#   make install-hooks — install git pre-commit hook

.PHONY: all gates build test test-all lint fix fix-check vet arch taste docs cover cover-gate install-hooks clean

# The lint tool version is pinned here (single source of truth);
# `make lint` self-installs it.
GOLANGCI_VERSION ?= v2.15.0
GOLANGCI := $(shell go env GOPATH)/bin/golangci-lint
GOFLAGS  ?= -race

all: gates

build:
	go build -o corec ./cmd/corec

test:
	go test $(GOFLAGS) -short -timeout 120s ./...

test-all:
	go test $(GOFLAGS) -timeout 300s ./...

lint:
	@test -x "$(GOLANGCI)" 2>/dev/null || { \
		echo ">> installing golangci-lint $(GOLANGCI_VERSION) (pinned; single source of truth)"; \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION); \
	}
	@$(GOLANGCI) version | head -1
	$(GOLANGCI) run --timeout 5m

vet:
	go vet ./...

# Code modernization — apply go fix to fixpoint (a single pass can enable
# further fixes, e.g. min/max nesting), then format.
fix:
	@for i in 1 2 3 4 5; do \
		go fix ./... || exit 1; \
		go fix -diff ./... >/dev/null 2>&1 && break; \
		[ $$i = 5 ] && { echo "❌ go fix did not reach a fixpoint in 5 rounds"; exit 1; }; \
	done
	gofmt -w .

# Modernization gate — the working tree must be a go fix fixpoint.
fix-check:
	go fix -diff ./...

# Architecture structure test — dependency direction enforcement
arch:
	go test -v ./archtest/

# Taste invariant tests — file size + structured logging
taste:
	go test -v ./tastetest/

# Document lint — dead links + required sections + orphan docs + package comments
docs:
	@bash scripts/check-docs.sh

# Coverage report
cover:
	go test -cover -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# Coverage gate — per-package + overall thresholds
cover-gate:
	@bash scripts/check-coverage.sh

# Tool-surface parity with the dashboard's AI assistant
# Skips (exit 0) when the sibling checkout is absent, so this repo builds alone.
parity:
	@node scripts/check-tools-parity.mjs --skip-if-missing

# All gates in one command — the canonical "is this commit safe?" check
gates: vet lint fix-check arch taste docs parity test cover-gate
	@echo ""
	@echo "✅ All gates passed"

# Install git pre-commit hook — fast subset: vet + pinned lint + fix-check
# + archtest + tastetest + doc lint. Race tests and coverage run at push/CI time.
install-hooks:
	@echo "#!/usr/bin/env bash" > .git/hooks/pre-commit
	@echo "set -e" >> .git/hooks/pre-commit
	@echo 'cd "$$(git rev-parse --show-toplevel)"' >> .git/hooks/pre-commit
	@echo "echo '--- pre-commit: fast gates (full gates run in CI) ---'" >> .git/hooks/pre-commit
	@echo "go vet ./... && make lint && make fix-check && go test ./archtest/ && go test ./tastetest/ && bash scripts/check-docs.sh" >> .git/hooks/pre-commit
	@echo 'echo "✅ pre-commit gates passed"' >> .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "✅ Pre-commit hook installed (.git/hooks/pre-commit)"
	@echo "   Runs: vet + lint(pinned v) + fix-check + arch + taste + docs — seconds, not minutes."

clean:
	rm -f corec cover.out
