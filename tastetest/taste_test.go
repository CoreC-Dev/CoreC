// File size上限 — no .go file exceeds maxLines without a documented exemption.
// Structured logging — no fmt.Println/fmt.Printf in production code.
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
// Each entry: file path → reason. Exemptions are conscious technical debt:
// every entry must also carry a SIZE PIN in exemptedFileCaps — growth
// beyond the pinned size fails the gate, shrinking lets you lower the
// pin, and a split that lands lets you delete the exemption entirely.
// The list is currently empty; keep it that way unless a file genuinely
// cannot be split yet.
var exemptedFiles = map[string]string{}

// exemptedFileCaps pins the maximum line count per exemption: growth
// beyond the pin fails the taste gate.
var exemptedFileCaps = map[string]int{}

func TestFileSizeLimit(t *testing.T) {
	root := findModuleRoot()
	violations := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // WalkDir: skip unreadable entries, don't abort walk
		}
		// Make path relative to module root
		relPath, _ := filepath.Rel(root, path)
		relPath = filepath.ToSlash(relPath) // normalize \ on Windows before prefix checks
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
			if cap, pinned := exemptedFileCaps[relPath]; pinned && lines > cap {
				t.Errorf(
					"VIOLATION: exempted file %s grew to %d lines (exemption pinned at %d)\n"+
						"  Fix: split the file and lower/remove the exemption, or raise the pin CONSCIOUSLY with a tracker update .\n"+
						"  Reason on file: %s",
					relPath, lines, cap, reason)
				violations++
			}
			return nil
		}

		t.Errorf(
			"VIOLATION: %s has %d lines (max %d)\n"+
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
		t.Fatalf("\n%d files exceed the %d-line limit without a documented exemption.\nSplit the file or register a documented exemption.", violations, maxLines)
	}
}

func TestStructuredLogging(t *testing.T) {
	root := findModuleRoot()
	// Ban fmt.Println and fmt.Printf in production code (not test, not demo, not log package itself)
	violations := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // WalkDir: skip unreadable entries, don't abort walk
		}
		relPath, _ := filepath.Rel(root, path)
		relPath = filepath.ToSlash(relPath) // normalize \ on Windows before prefix checks
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
					"VIOLATION: %s:%d uses fmt.Println/Printf for output\n"+
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
