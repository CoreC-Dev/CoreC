package log

import (
	"log/slog"
	"testing"
	"time"
)

func TestSubscribeAndPublish(t *testing.T) {
	Init(slog.LevelInfo)
	defer Init(slog.LevelInfo)

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
	Init(slog.LevelWarn)

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

	Init(slog.LevelInfo)
}

func TestSetLevel(t *testing.T) {
	Init(slog.LevelInfo)

	SetLevel(slog.LevelError)
	if Level() != slog.LevelError {
		t.Errorf("expected level=Error, got %v", Level())
	}

	SetLevel(slog.LevelDebug)
	if Level() != slog.LevelDebug {
		t.Errorf("expected level=Debug, got %v", Level())
	}

	Init(slog.LevelInfo)
}

func TestMultipleSubscribers(t *testing.T) {
	Init(slog.LevelInfo)

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
	Init(slog.LevelInfo)

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

	Init(slog.LevelInfo)
}

func TestConvenienceFunctions(t *testing.T) {
	Init(slog.LevelInfo)

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
	Init(slog.LevelInfo)
	defer Init(slog.LevelInfo)

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
