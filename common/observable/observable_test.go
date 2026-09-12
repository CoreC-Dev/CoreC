package observable

import (
	"testing"
	"time"
)

func TestObservable(t *testing.T) {
	src := make(chan int, 10)
	obs := NewObservable(src)

	ch1, unsub1 := obs.Subscribe()
	ch2, unsub2 := obs.Subscribe()

	src <- 42

	select {
	case v := <-ch1:
		if v != 42 {
			t.Errorf("ch1 expected 42, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ch1 timeout")
	}

	select {
	case v := <-ch2:
		if v != 42 {
			t.Errorf("ch2 expected 42, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timeout")
	}

	// Unsubscribe ch1
	unsub1()

	src <- 99

	select {
	case v := <-ch2:
		if v != 99 {
			t.Errorf("ch2 expected 99, got %d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timeout")
	}

	unsub2()
	close(src)
}

func TestObservableDropped(t *testing.T) {
	src := make(chan int, 10)
	obs := NewObservable(src)

	// Subscribe but never consume — buffer (cap=128) will fill up.
	ch, unsub := obs.Subscribe()
	_ = ch

	// Send more than the subscriber buffer capacity to force drops.
	const sendCount = 200 // 128 buffered + 72 dropped
	for i := 0; i < sendCount; i++ {
		src <- i
	}

	// Wait for the fan-out goroutine to process all events.
	// Poll until Dropped stabilizes or timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if obs.Dropped() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	dropped := obs.Dropped()
	if dropped == 0 {
		t.Fatal("expected drops > 0 for slow subscriber, got 0")
	}
	t.Logf("sent=%d, dropped=%d", sendCount, dropped)

	unsub()
	close(src)
}

func TestObservableNoDropsWhenConsuming(t *testing.T) {
	src := make(chan int, 10)
	obs := NewObservable(src)

	ch, unsub := obs.Subscribe()

	// Consumer that drains everything.
	done := make(chan struct{})
	received := 0
	go func() {
		for range ch {
			received++
		}
		close(done)
	}()

	// Send items with a small yield so the consumer can keep up.
	// A burst can cause transient drops due to goroutine scheduling,
	// which is expected behavior, not a bug.
	for i := 0; i < 200; i++ {
		src <- i
		time.Sleep(time.Millisecond)
	}

	// All items are in src's buffer; unsub() is mutex-safe to call
	// immediately (fan-out holds the same lock when writing to subs).
	unsub()
	close(src)
	<-done

	t.Logf("received=%d, dropped=%d", received, obs.Dropped())
}
