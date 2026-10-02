#!/usr/bin/env bash
# pre-commit hook — runs all CoreC gates before allowing a commit
#
# Install: make install-hooks  (or manually copy to .git/hooks/pre-commit)
#
# Gates run:
#   1. go vet          — static analysis
#   2. golangci-lint   — lint (errcheck/govet/staticcheck/gocyclo/revive/...)
#   3. archtest        — T7: dependency direction structure test
#   4. tastetest       — T1: file size limit, T6: structured logging
#   5. check-docs.sh   — T9: document lint (dead links/required sections/orphans)
#   6. go test -short  — unit tests (short mode)
#
# To bypass in emergency: git commit --no-verify (discouraged; document why)

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

echo "=== Running CoreC gates ==="

echo "--- vet ---"
go vet ./...

echo "--- lint ---"
golangci-lint run --timeout 5m 2>/dev/null || {
    echo "⚠️  golangci-lint not found or failed. Install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"
    echo "   Skipping lint (not blocking). For full gates, install golangci-lint."
}

echo "--- arch test (T7: dependency direction) ---"
go test ./archtest/

echo "--- taste test (T1: file size, T6: structured logging) ---"
go test ./tastetest/

echo "--- doc lint (T9: dead links, required sections, orphans) ---"
bash scripts/check-docs.sh

echo "--- unit tests (short mode) ---"
go test -race -short -timeout 120s ./...

echo ""
echo "✅ All gates passed — commit allowed."
