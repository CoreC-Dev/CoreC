package log

import (
	"log/slog"
	"testing"
)

// TEST-008: ParseLevel table-driven test covering all recognized levels,
// case sensitivity, and unrecognized input.

func TestParseLevel(t *testing.T) {
	cases := []struct {
		input string
		want  slog.Level
		ok    bool
	}{
		{"debug", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"silent", LevelSilent, true},
		// Case-sensitive: uppercase variants are not recognized.
		{"DEBUG", slog.LevelInfo, false},
		{"Info", slog.LevelInfo, false},
		{"", slog.LevelInfo, false},
		{"trace", slog.LevelInfo, false},
		{"fatal", slog.LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.input)
		if ok != c.ok {
			t.Errorf("ParseLevel(%q) ok = %v, want %v", c.input, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestSetGetLevelRoundTrip(t *testing.T) {
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		SetLevel(lvl)
		if got := Level(); got != lvl {
			t.Errorf("Level() after SetLevel(%v) = %v", lvl, got)
		}
	}
}
