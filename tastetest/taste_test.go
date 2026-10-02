// Package tastetest contains taste invariant tests (T1–T10) that mechanically enforce
// the style and architecture conventions defined in docs/design-docs/core-beliefs.md.
//
// T1: File size上限 — no .go file exceeds maxLines without a documented exemption.
// T6: Structured logging — no fmt.Println/fmt.Printf in production code.
// T8: Platform reliability — every go statement must be context-bound (checked via grep).
package tastetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const maxLines = 600

// findModuleRoot walks up from cwd to find the directory containing go.mod.
func findModuleRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("go.mod not found")
		}
		dir = parent
	}
}

// exemptedFiles lists files that exceed maxLines with a documented reason.
// Each entry: file path → reason. These are technical debt items being tracked
// in tech-debt-tracker.md and will be resolved in Phase 4.
var exemptedFiles = map[string]string{
	"transport/mqtt/publisher.go":  "CPLX-001: 1192 lines, Phase 4 batch 7 will split into replay_window/command_handler/tls_config/publisher",
	"engine/engine.go":             "CPLX-003: 875 lines, Phase 4 batch 3 will split into lifecycle/stats/config",
	"transport/httppush/push.go":   "CPLX-009: 759 lines, Phase 4 batch 8 will split into webhook/push_config",
	"hub/route/metrics.go":         "CPLX-011: 679 lines, Phase 4 batch 10 will split by metric family",
	"driver/s7/s7.go":              "CPLX-004: 645 lines, Phase 4 batch 6 will split into address/codec/lifecycle",
	"hub/executor/executor.go":     "CPLX-012: 622 lines, Phase 4 batch 11 will split into diff/apply",
	"engine/batcher.go":            "CPLX-014: 617 lines, Phase 4 batch 3 will split into retry_buffer",
}

func TestFileSizeLimit(t *testing.T) {
	root := findModuleRoot()
	violations := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // WalkDir: skip unreadable entries, don't abort walk
		}
		// Make path relative to module root
		relPath, _ := filepath.Rel(root, path)
		// Only check .go files
		if !strings.HasSuffix(relPath, ".go") {
			return nil
		}
		// Skip test files, vendor, .git, node_modules, archtest, tastetest
		if strings.HasSuffix(relPath, "_test.go") ||
			strings.Contains(relPath, "vendor/") ||
			strings.Contains(relPath, ".git/") ||
			strings.Contains(relPath, "node_modules/") ||
			strings.HasPrefix(relPath, "archtest/") ||
			strings.HasPrefix(relPath, "tastetest/") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // skip unreadable files, don't abort walk
		}
		lines := strings.Count(string(data), "\n") + 1
		if lines <= maxLines {
			return nil
		}

		reason, exempted := exemptedFiles[relPath]
		if exempted {
			return nil // documented exemption
		}

		t.Errorf(
			"T1 VIOLATION: %s has %d lines (max %d)\n"+
				"  Fix: Split this file into smaller files by responsibility. If splitting is not yet possible, add an entry to exemptedFiles in tastetest/taste_test.go with a reason and a tracker ID.\n"+
				"  Note: If this is a new file, it should be born under %d lines.",
			relPath, lines, maxLines, maxLines)
		violations++
		_ = reason
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if violations > 0 {
		t.Fatalf("\n%d files exceed the %d-line limit without a documented exemption.\nSee docs/exec-plans/tech-debt-tracker.md CPLX entries for remediation plans.", violations, maxLines)
	}
}

func TestStructuredLogging(t *testing.T) {
	root := findModuleRoot()
	// T6: Ban fmt.Println and fmt.Printf in production code (not test, not demo, not log package itself)
	violations := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // WalkDir: skip unreadable entries, don't abort walk
		}
		relPath, _ := filepath.Rel(root, path)
		if !strings.HasSuffix(relPath, ".go") {
			return nil
		}
		// Skip test files, vendor, .git, demo, log package, cmd (CLI output), archtest, tastetest
		if strings.HasSuffix(relPath, "_test.go") ||
			strings.Contains(relPath, "vendor/") ||
			strings.Contains(relPath, ".git/") ||
			strings.HasPrefix(relPath, "demo/") ||
			strings.HasPrefix(relPath, "log/") ||
			strings.HasPrefix(relPath, "archtest/") ||
			strings.HasPrefix(relPath, "tastetest/") ||
			strings.HasPrefix(relPath, "cmd/corec/") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // skip unreadable files, don't abort walk
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if strings.Contains(line, "fmt.Println") || strings.Contains(line, "fmt.Printf") {
				t.Errorf(
					"T6 VIOLATION: %s:%d uses fmt.Println/Printf for output\n"+
						"  Fix: Use slog (via the log package) for structured logging instead of fmt.Print*. Example: log.Info(\"message\", \"key\", value) instead of fmt.Printf(\"message %%v\\n\", value).\n"+
						"  Why: Structured logs are searchable, filterable, and won't break log aggregation. fmt.Print* bypasses the logging pipeline.",
					relPath, i+1)
				violations++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if violations > 0 {
		t.Fatalf("\n%d uses of fmt.Println/Printf found in production code.\nUse slog (via the log package) for all logging.", violations)
	}
}
