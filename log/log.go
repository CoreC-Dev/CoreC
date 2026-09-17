package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/observable"
)

// Event represents a log event for subscribers.
type Event struct {
	Level     slog.Level `json:"level"`
	Type      string     `json:"type"` // "info", "warning", etc.
	Payload   string     `json:"payload"`
	Timestamp time.Time  `json:"timestamp"`
}

// DefaultLogBufferSize is the default capacity of the source log event channel.
const DefaultLogBufferSize = 1024

var (
	logCh  chan Event
	source *observable.Observable[Event]

	mu    sync.RWMutex
	level = slog.LevelInfo

	// sourceDropped counts events dropped at the source channel (logCh full)
	// before they even reach the Observable fan-out.
	sourceDropped atomic.Int64

	initOnce sync.Once
)

// InitBuffer initializes the log event channel with the given buffer size.
// If size <= 0, DefaultLogBufferSize is used. Must be called before Init()
// or any logging produces events; subsequent calls are no-ops. If never
// called, the channel is lazily initialized with the default size on first use.
func InitBuffer(bufferSize int) {
	initOnce.Do(func() {
		if bufferSize <= 0 {
			bufferSize = DefaultLogBufferSize
		}
		logCh = make(chan Event, bufferSize)
		source = observable.NewObservable[Event](logCh)
	})
}

func ensureInit() {
	initOnce.Do(func() {
		logCh = make(chan Event, DefaultLogBufferSize)
		source = observable.NewObservable[Event](logCh)
	})
}

// Dropped returns the total number of log events dropped due to slow consumers.
// This includes both source-channel drops (logCh full) and subscriber fan-out
// drops (individual subscriber buffer full).
func Dropped() int64 {
	ensureInit()
	return sourceDropped.Load() + source.Dropped()
}

// Subscribe returns a channel of log events and an unsubscribe function.
func Subscribe() (ch <-chan Event, unsub func()) {
	ensureInit()
	return source.Subscribe()
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size.
func SubscribeWithBuffer(size int) (ch <-chan Event, unsub func()) {
	ensureInit()
	return source.SubscribeWithBuffer(size)
}

// Level returns the current global log level.
func Level() slog.Level {
	mu.RLock()
	defer mu.RUnlock()
	return level
}

// SetLevel sets the global log level.
func SetLevel(l slog.Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// Init initializes the global slog logger with an observable handler
// that writes to stdout and publishes events to all subscribers.
// This ensures ALL slog.Info/slog.Error calls throughout the codebase
// are captured by the log bus — no code changes needed.
//
// format selects the console output format: "json" produces structured
// JSON lines via slog.NewJSONHandler; any other value (including the
// default "") falls back to slog.NewTextHandler. The published Event
// bus payload is unaffected by format — only the stdout rendering.
func Init(l slog.Level, format string) {
	mu.Lock()
	level = l
	mu.Unlock()

	handler := NewObservableHandler(os.Stdout, l, format)
	slog.SetDefault(slog.New(handler))
}

// ObservableHandler is a slog.Handler that writes to an underlying handler
// and simultaneously publishes structured events to the log bus.
type ObservableHandler struct {
	inner slog.Handler
}

// NewObservableHandler creates a new ObservableHandler wrapping a handler
// selected by format. When format is "json" (case-insensitive) the inner
// handler is a slog.JSONHandler; otherwise a slog.TextHandler is used.
// The returned handler publishes Event records to the log bus regardless
// of the chosen console format.
func NewObservableHandler(w io.Writer, l slog.Level, format string) *ObservableHandler {
	opts := &slog.HandlerOptions{Level: l}
	var inner slog.Handler
	if strings.EqualFold(format, "json") {
		inner = slog.NewJSONHandler(w, opts)
	} else {
		inner = slog.NewTextHandler(w, opts)
	}
	return &ObservableHandler{inner: inner}
}

// Enabled implements slog.Handler.
func (h *ObservableHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return h.inner.Enabled(context.Background(), lvl)
}

// Handle implements slog.Handler. It writes to the inner handler and
// publishes an Event to the log bus.
func (h *ObservableHandler) Handle(ctx context.Context, r slog.Record) error {
	// Publish to subscribers
	publishRecord(r)

	// Write to stdout via inner handler
	return h.inner.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *ObservableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ObservableHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *ObservableHandler) WithGroup(name string) slog.Handler {
	return &ObservableHandler{inner: h.inner.WithGroup(name)}
}

func publishRecord(r slog.Record) {
	var typ string

	switch {
	case r.Level >= slog.LevelError:
		typ = "error"
	case r.Level >= slog.LevelWarn:
		typ = "warning"
	case r.Level >= slog.LevelInfo:
		typ = "info"
	default:
		typ = "debug"
	}

	mu.RLock()
	currentLevel := level
	mu.RUnlock()

	if r.Level < currentLevel {
		return
	}

	// Build payload: message + key=value pairs
	var sb strings.Builder
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		sb.WriteString(a.Value.String())
		return true
	})

	ev := Event{
		Level:     r.Level,
		Type:      typ,
		Payload:   sb.String(),
		Timestamp: r.Time,
	}

	ensureInit()
	select {
	case logCh <- ev:
	default:
		// channel full, drop message
		sourceDropped.Add(1)
	}
}

// --- Convenience functions (printf-style wrappers around slog) ---

func pushLog(l slog.Level, format string, v ...any) {
	mu.RLock()
	currentLevel := level
	mu.RUnlock()

	if l < currentLevel {
		return
	}

	payload := fmt.Sprintf(format, v...)

	// Route through slog so ObservableHandler handles both console output
	// and event bus publishing. Do NOT send to logCh directly here —
	// that would double-publish since ObservableHandler also sends to logCh.
	switch {
	case l >= slog.LevelError:
		slog.Error(payload)
	case l >= slog.LevelWarn:
		slog.Warn(payload)
	case l >= slog.LevelInfo:
		slog.Info(payload)
	default:
		slog.Debug(payload)
	}
}

// Infoln logs a message at INFO level.
func Infoln(format string, v ...any) {
	pushLog(slog.LevelInfo, format, v...)
}

// Warnln logs a message at WARNING level.
func Warnln(format string, v ...any) {
	pushLog(slog.LevelWarn, format, v...)
}

// Errorln logs a message at ERROR level.
func Errorln(format string, v ...any) {
	pushLog(slog.LevelError, format, v...)
}

// Debugln logs a message at DEBUG level.
func Debugln(format string, v ...any) {
	pushLog(slog.LevelDebug, format, v...)
}
