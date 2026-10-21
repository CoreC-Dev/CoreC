# CoreC Makefile — one-command gate reproduction
#
# Usage:
#   make gates       — run all gates (lint + vet + test + arch + taste + docs + cover)
#   make build       — build the binary
#   make test        — run short tests with race detector
#   make lint        — run golangci-lint (auto-installs the pinned version)
#   make arch        — run architecture structure tests
#   make taste       — run taste invariant tests
#   make docs        — run document lint
#   make cover       — generate coverage report
#   make install-hooks — install git pre-commit hook

.PHONY: all gates build test test-all lint vet arch taste docs cover cover-gate install-hooks clean

# H1 (quality audit 2026-10-21): the lint tool version is pinned HERE —
# the single source of truth — and `make lint` self-installs it, so a
# fresh clone mechanically reproduces CI's exact lint behavior. (CI
# previously pinned v2.14.0 in workflow YAML only; local runs used
# whatever was on PATH, and the repo's old hook silently skipped lint
# when it was missing — the root cause of 8+ recurring fix-lint commits.)
GOLANGCI_VERSION ?= v2.14.0
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

# T7: Architecture structure test — dependency direction enforcement
arch:
	go test -v ./archtest/

# T1/T6: Taste invariant tests — file size + structured logging
taste:
	go test -v ./tastetest/

# T9: Document lint — dead links + required sections + orphan docs + package comments
docs:
	@bash scripts/check-docs.sh

# T10: Coverage report
cover:
	go test -cover -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# T11: Coverage gate — per-package + overall thresholds (Phase 5)
cover-gate:
	@bash scripts/check-coverage.sh

# All gates in one command — the canonical "is this commit safe?" check
gates: vet lint arch taste docs test cover-gate
	@echo ""
	@echo "✅ All gates passed"

# Install git pre-commit hook — the FAST tier (seconds, ~100% compliance):
# vet + pinned lint (golangci-lint caches, so only changed code costs time)
# + archtest + tastetest + doc lint. Full `make gates` (race tests,
# coverage) stays at push/CI time: a 3-minute hook gets mass-bypassed
# with --no-verify, which is worse than no hook (audit H1 rationale).
install-hooks:
	@echo "#!/usr/bin/env bash" > .git/hooks/pre-commit
	@echo "set -e" >> .git/hooks/pre-commit
	@echo 'cd "$$(git rev-parse --show-toplevel)"' >> .git/hooks/pre-commit
	@echo "echo '--- pre-commit: fast gates (full gates run in CI) ---'" >> .git/hooks/pre-commit
	@echo "go vet ./... && make lint && go test ./archtest/ && go test ./tastetest/ && bash scripts/check-docs.sh" >> .git/hooks/pre-commit
	@echo 'echo "✅ pre-commit gates passed"' >> .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "✅ Pre-commit hook installed (.git/hooks/pre-commit)"
	@echo "   Runs: vet + lint(pinned v) + arch + taste + docs — seconds, not minutes."

clean:
	rm -f corec cover.out
