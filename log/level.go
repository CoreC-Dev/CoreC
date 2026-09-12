package log

import "log/slog"

// LevelSilent suppresses all logging. It is higher than slog.LevelError
// so no record passes the Enabled check.
const LevelSilent = slog.LevelError + 10

// LevelMapping maps string log levels to slog.Level values.
var LevelMapping = map[string]slog.Level{
	"debug":   slog.LevelDebug,
	"info":    slog.LevelInfo,
	"warn":    slog.LevelWarn,
	"warning": slog.LevelWarn,
	"error":   slog.LevelError,
	"silent":  LevelSilent,
}

// ParseLevel converts a string to an slog.Level. Returns ok=false for
// unrecognized strings.
func ParseLevel(s string) (slog.Level, bool) {
	l, ok := LevelMapping[s]
	return l, ok
}
