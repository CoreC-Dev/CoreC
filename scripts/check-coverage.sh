#!/usr/bin/env bash
# Coverage gate — fails if overall or per-package coverage drops below
# the configured thresholds. Integrated into `make gates` (Phase 5).
#
# Usage: bash scripts/check-coverage.sh
# Exit: 0 if all thresholds met, 1 otherwise.

set -uo pipefail

echo "── Coverage gate ──────────────────────────────────────────────"

# Run coverage across all packages.
COVERPROFILE="${COVERPROFILE:-cover.out}"
GOFLAGS="${GOFLAGS:-}"
go test $GOFLAGS -cover -coverprofile="$COVERPROFILE" -timeout 300s ./... 2>&1 | \
  grep -E "^ok" > /tmp/corec-pkg-cov.txt

# Helper: returns 0 if $1 < $2 (decimal comparison).
below() { awk "BEGIN{exit !($1 < $2)}"; }

# Overall threshold.
OVERALL_MIN=75
OVERALL=$(go tool cover -func="$COVERPROFILE" | tail -1 | awk '{print $NF}' | tr -d '%')
echo "Overall coverage: ${OVERALL}% (min ${OVERALL_MIN}%)"
if below "$OVERALL" "$OVERALL_MIN"; then
  echo "❌ Overall coverage ${OVERALL}% < ${OVERALL_MIN}%"
  exit 1
fi

# Per-package threshold lookup.
threshold_for() {
  case "$1" in
    core)              echo 95 ;;
    config)            echo 80 ;;
    engine)            echo 75 ;;
    engine/statistic)  echo 95 ;;
    hub)               echo 90 ;;
    hub/executor)      echo 75 ;;
    hub/route)         echo 85 ;;
    log)               echo 75 ;;
    rule)              echo 80 ;;
    common/metrics)    echo 90 ;;
    common/observable) echo 90 ;;
    common/trace)      echo 90 ;;
    common/util)       echo 90 ;;
    transport/parser)  echo 85 ;;
    transport/mqtt)    echo 70 ;;
    transport/httppush) echo 85 ;;
    driver/modbus)     echo 70 ;;
    driver/opcua)      echo 45 ;;
    driver/s7)         echo 80 ;;
    *)                 echo "" ;;
  esac
}

FAILED=0
while IFS= read -r line; do
  # Parse: "ok  \tpkg\ttime\tcoverage: N.N% of statements"
  pkg=$(echo "$line" | awk '{print $2}')
  pct=$(echo "$line" | grep -oE '[0-9]+\.[0-9]+%' | head -1 | tr -d '%')
  [[ -z "$pct" ]] && continue  # no statements

  suffix="${pkg#github.com/CoreC-Dev/CoreC/}"
  [[ "$suffix" == "$pkg" ]] && suffix="root"

  min=$(threshold_for "$suffix")
  [[ -z "$min" ]] && continue  # no threshold defined for this package

  if below "$pct" "$min"; then
    echo "❌ ${suffix}: ${pct}% < ${min}%"
    FAILED=1
  else
    echo "✅ ${suffix}: ${pct}% (min ${min}%)"
  fi
done < /tmp/corec-pkg-cov.txt

if [[ "$FAILED" -eq 0 ]]; then
  echo "✅ All coverage thresholds met"
else
  echo "❌ Some coverage thresholds not met"
  exit 1
fi
