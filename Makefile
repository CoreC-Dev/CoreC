# CoreC Makefile — one-command gate reproduction
#
# Usage:
#   make gates       — run all gates (lint + vet + test + arch + taste + docs + cover)
#   make build       — build the binary
#   make test        — run short tests with race detector
#   make lint        — run golangci-lint
#   make arch        — run architecture structure tests
#   make taste       — run taste invariant tests
#   make docs        — run document lint
#   make cover       — generate coverage report
#   make install-hooks — install git pre-commit hook

.PHONY: all gates build test test-all lint vet arch taste docs cover install-hooks clean

GOLANGCI ?= golangci-lint
GOFLAGS  ?= -race

all: gates

build:
	go build -o corec ./cmd/corec

test:
	go test $(GOFLAGS) -short -timeout 120s ./...

test-all:
	go test $(GOFLAGS) -timeout 300s ./...

lint:
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

# All gates in one command — the canonical "is this commit safe?" check
gates: vet lint arch taste docs test
	@echo ""
	@echo "✅ All gates passed"

# Install git pre-commit hook
install-hooks:
	@echo "#!/usr/bin/env bash" > .git/hooks/pre-commit
	@echo "make gates" >> .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo "✅ Pre-commit hook installed (.git/hooks/pre-commit)"
	@echo "   Runs: vet + lint + arch + taste + docs + test"

clean:
	rm -f corec cover.out
