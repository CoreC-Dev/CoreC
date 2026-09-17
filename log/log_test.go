package log

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSubscribeAndPublish(t *testing.T) {
	Init(slog.LevelInfo, "text")
	defer Init(slog.LevelInfo, "text")

	ch, unsub := Subscribe()
	defer unsub()

	slog.Info("test message", "key", "value")

	select {
	case ev := <-ch:
		if ev.Type != "info" {
			t.Errorf("expected type=info, got %s", ev.Type)
		}
		if ev.Payload == "" {
			t.Error("expected non-empty payload")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for log event")
	}
}

func TestLevelFiltering(t *testing.T) {
	Init(slog.LevelWarn, "text")

	ch, unsub := Subscribe()
	defer unsub()

	slog.Info("should be filtered out")
	slog.Warn("should pass through")

	// Blocking wait with timeout (replaces sleep + non-blocking select).
	select {
	case ev := <-ch:
		if ev.Type != "warning" {
			t.Errorf("expected type=warning, got %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for warning event")
	}

	// Info was filtered out — channel should be empty.
	select {
	case ev := <-ch:
		t.Errorf("should not have received info event, got: %+v", ev)
	default:
	}

	Init(slog.LevelInfo, "text")
}

func TestSetLevel(t *testing.T) {
	Init(slog.LevelInfo, "text")

	SetLevel(slog.LevelError)
	if Level() != slog.LevelError {
		t.Errorf("expected level=Error, got %v", Level())
	}

	SetLevel(slog.LevelDebug)
	if Level() != slog.LevelDebug {
		t.Errorf("expected level=Debug, got %v", Level())
	}

	Init(slog.LevelInfo, "text")
}

func TestMultipleSubscribers(t *testing.T) {
	Init(slog.LevelInfo, "text")

	ch1, unsub1 := Subscribe()
	ch2, unsub2 := Subscribe()
	defer unsub1()
	defer unsub2()

	slog.Info("broadcast message")

	received := 0
	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case <-ch:
			received++
		case <-time.After(time.Second):
		}
	}

	if received != 2 {
		t.Errorf("expected 2 subscribers to receive, got %d", received)
	}
}

func TestUnsubscribe(t *testing.T) {
	Init(slog.LevelInfo, "text")

	ch, unsub := Subscribe()

	slog.Info("before unsubscribe")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Error("timeout waiting for event before unsubscribe")
	}

	unsub()

	slog.Info("after unsubscribe")
	select {
	case ev, ok := <-ch:
		if ok && ev.Payload != "" {
			t.Errorf("should not receive after unsubscribe, got: %+v", ev)
		}
	// ok==false means channel closed, which is expected
	case <-time.After(50 * time.Millisecond):
	}

	Init(slog.LevelInfo, "text")
}

func TestConvenienceFunctions(t *testing.T) {
	Init(slog.LevelInfo, "text")

	ch, unsub := Subscribe()
	defer unsub()

	Infoln("info test %d", 1)
	Warnln("warn test %d", 2)
	Errorln("error test %d", 3)

	count := 0
	for count < 3 {
		select {
		case <-ch:
			count++
		case <-time.After(time.Second):
			t.Errorf("timeout after receiving %d/3 events", count)
			return
		}
	}

	if count != 3 {
		t.Errorf("expected 3 events, got %d", count)
	}
}

func TestDropped(t *testing.T) {
	Init(slog.LevelInfo, "text")
	defer Init(slog.LevelInfo, "text")

	// Subscribe but never consume — buffer will fill up and cause drops.
	ch, unsub := Subscribe()
	_ = ch

	// Generate enough log events to overflow the subscriber buffer (cap=128)
	// and the source channel (cap=1024).
	for i := 0; i < 2000; i++ {
		Infoln("flood %d", i)
	}

	// Wait for drop counter to register (poll with timeout).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if Dropped() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	dropped := Dropped()
	if dropped == 0 {
		t.Fatal("expected drops > 0 for slow subscriber, got 0")
	}
	t.Logf("dropped=%d", dropped)

	unsub()
}

// TestDroppedSumsCounters verifies that Dropped() reports the exact total
// of source-channel drops and subscriber fan-out drops. It is deterministic
// and complements the flood-style TestDropped above.
func TestDroppedSumsCounters(t *testing.T) {
	Init(slog.LevelInfo, "text")
	defer Init(slog.LevelInfo, "text")
	ensureInit()

	// Snapshot both underlying counters; Dropped() must equal their sum.
	sourceBefore := sourceDropped.Load()
	obsBefore := source.Dropped()
	want := sourceBefore + obsBefore
	if got := Dropped(); got != want {
		t.Fatalf("Dropped()=%d, want %d (source=%d + obs=%d)",
			got, want, sourceBefore, obsBefore)
	}

	// Bump the source-drop counter exactly once and confirm Dropped()
	// reflects the new total — i.e. it reads the live counters, not a cache.
	sourceDropped.Add(1)
	want = (sourceBefore + 1) + obsBefore
	if got := Dropped(); got != want {
		t.Fatalf("after source drop: Dropped()=%d, want %d", got, want)
	}
}

// TestNewObservableHandlerJSONFormat verifies that the "json" format wraps a
// slog.JSONHandler and emits structured JSON to the writer.
func TestNewObservableHandlerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewObservableHandler(&buf, slog.LevelInfo, "json")
	logger := slog.New(h)
	logger.Info("hello", "key", "value")

	out := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(out, "{") {
		t.Fatalf("expected JSON object, got: %q", out)
	}
	if !strings.Contains(out, `"msg":"hello"`) {
		t.Errorf("expected JSON to contain msg field, got: %q", out)
	}
	if !strings.Contains(out, `"key":"value"`) {
		t.Errorf("expected JSON to contain attr, got: %q", out)
	}
}

// TestNewObservableHandlerTextFormat verifies that the "text" format wraps a
// slog.TextHandler and emits key=value text to the writer.
func TestNewObservableHandlerTextFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewObservableHandler(&buf, slog.LevelInfo, "text")
	logger := slog.New(h)
	logger.Info("hello", "key", "value")

	out := strings.TrimSpace(buf.String())
	if strings.HasPrefix(out, "{") {
		t.Fatalf("expected text output, got JSON: %q", out)
	}
	if !strings.Contains(out, "msg=hello") {
		t.Errorf("expected text to contain msg=hello, got: %q", out)
	}
	if !strings.Contains(out, "key=value") {
		t.Errorf("expected text to contain key=value, got: %q", out)
	}
}

// TestNewObservableHandlerDefaultFormat verifies that an empty or unknown
// format falls back to the text handler (backward-compatible default).
func TestNewObservableHandlerDefaultFormat(t *testing.T) {
	for _, format := range []string{"", "text", "TEXT", "bogus"} {
		var buf bytes.Buffer
		h := NewObservableHandler(&buf, slog.LevelInfo, format)
		slog.New(h).Info("hello")

		out := strings.TrimSpace(buf.String())
		if strings.HasPrefix(out, "{") {
			t.Errorf("format %q: expected text fallback, got JSON: %q", format, out)
		}
		if !strings.Contains(out, "msg=hello") {
			t.Errorf("format %q: expected msg=hello, got: %q", format, out)
		}
	}
}

// TestNewObservableHandlerJSONCaseInsensitive verifies that "JSON" (uppercase)
// is accepted as JSON format.
func TestNewObservableHandlerJSONCaseInsensitive(t *testing.T) {
	var buf bytes.Buffer
	h := NewObservableHandler(&buf, slog.LevelInfo, "JSON")
	slog.New(h).Info("hello")

	out := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(out, "{") {
		t.Errorf("expected JSON for uppercase format, got: %q", out)
	}
}

// TestInitJSONFormat verifies that Init with "json" configures the global
// slog logger to emit JSON lines on stdout.
func TestInitJSONFormat(t *testing.T) {
	defer Init(slog.LevelInfo, "text") // restore defaults for other tests

	out := captureStdout(t, func() {
		Init(slog.LevelInfo, "json")
		slog.Info("json-test", "k", "v")
	})

	if !strings.Contains(out, `"msg":"json-test"`) {
		t.Errorf("expected JSON output from Init, got: %q", out)
	}
}

// TestInitTextFormat verifies that Init with "text" configures the global
// slog logger to emit text lines on stdout.
func TestInitTextFormat(t *testing.T) {
	defer Init(slog.LevelInfo, "text")

	out := captureStdout(t, func() {
		Init(slog.LevelInfo, "text")
		slog.Info("text-test", "k", "v")
	})

	if strings.Contains(out, `"msg":"text-test"`) {
		t.Errorf("expected text output from Init, got JSON: %q", out)
	}
	if !strings.Contains(out, "msg=text-test") {
		t.Errorf("expected text output to contain msg=text-test, got: %q", out)
	}
}

// captureStdout temporarily replaces os.Stdout with a pipe, runs fn, and
// returns everything written. Init reads os.Stdout at call time, so it must
// be invoked inside fn for the captured writer to take effect.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan struct{})
	var captured string
	go func() {
		defer close(done)
		b, _ := io.ReadAll(r)
		captured = string(b)
	}()

	fn()
	_ = w.Close()
	<-done
	return captured
}
