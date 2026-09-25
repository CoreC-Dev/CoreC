package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/engine/statistic"
	"github.com/CoreC-Dev/CoreC/rule"
)

// TestDataBusHighPriorityChannel verifies that the high-priority channel is
// distinct from the main channel and that PushHighPriority routes data
// exclusively to it (IMPROVEMENTS #5).
func TestDataBusHighPriorityChannel(t *testing.T) {
	bus := NewDataBus(256)
	defer bus.Close()

	// The two channels must be different underlying channels.
	if bus.Channel() == bus.HighPriorityChannel() {
		t.Fatal("HighPriorityChannel must be distinct from Channel")
	}

	// Push to the main channel — high-priority channel must stay empty.
	bus.Push(core.DataPoint{Driver: "d", Tag: "normal", Value: 1.0})
	select {
	case <-bus.HighPriorityChannel():
		t.Fatal("normal Push must not send to the high-priority channel")
	default:
	}
	// Drain the normal point from the main channel so the later
	// assertion can distinguish a PushHighPriority leak from this point.
	<-bus.Channel()

	// Push to the high-priority channel — main channel must not receive it.
	bus.PushHighPriority(core.DataPoint{Driver: "d", Tag: "hp", Value: 2.0})
	select {
	case pt := <-bus.HighPriorityChannel():
		if pt.Tag != "hp" {
			t.Fatalf("high-priority point tag = %q, want hp", pt.Tag)
		}
	case <-time.After(time.Second):
		t.Fatal("PushHighPriority did not deliver to HighPriorityChannel")
	}
	select {
	case <-bus.Channel():
		t.Fatal("PushHighPriority must not send to the main channel")
	default:
	}
}

// TestDataBusHighPriorityPushDropped verifies that the high-priority channel
// counts drops separately when its buffer is full and the drop-oldest
// eviction fires.
func TestDataBusHighPriorityPushDropped(t *testing.T) {
	bus := NewDataBus(64) // high-pri buffer = 64/4 = 16
	defer bus.Close()

	hpCap := cap(bus.highPriCh)

	// Fill the high-priority channel to capacity.
	for i := 0; i < hpCap; i++ {
		bus.PushHighPriority(core.DataPoint{Driver: "d", Tag: fmt.Sprintf("t%d", i), Value: float64(i)})
	}
	if got := bus.HighPriorityPushDropped(); got != 0 {
		t.Fatalf("HighPriorityPushDropped = %d before overflow, want 0", got)
	}

	// Overflow: the oldest entry should be evicted.
	bus.PushHighPriority(core.DataPoint{Driver: "d", Tag: "overflow", Value: 999})
	if got := bus.HighPriorityPushDropped(); got != 1 {
		t.Fatalf("HighPriorityPushDropped = %d after 1 overflow, want 1", got)
	}

	// The overflow point must be in the channel (it replaced the oldest).
	found := false
	drain := 0
	for {
		select {
		case pt := <-bus.HighPriorityChannel():
			drain++
			if pt.Tag == "overflow" {
				found = true
			}
		default:
			if !found {
				t.Fatal("overflow point not found in high-priority channel after eviction")
			}
			if drain != hpCap {
				t.Fatalf("drained %d points, expected %d", drain, hpCap)
			}
			return
		}
	}
}

// TestEngineHighPriorityWorkersProcessData is an integration test verifying
// that high-priority data pushed via PushHighPriority is processed by the
// dedicated high-priority workers even when the main channel is saturated
// with normal-priority data (IMPROVEMENTS #5).
func TestEngineHighPriorityWorkersProcessData(t *testing.T) {
	e := New().(*CoreCEngine)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		e.cancel()
		e.wg.Wait()
	})

	e.dataBus = NewDataBus(4096)
	e.cache = NewLatestCache()
	e.ruleEngine = rule.NewEngine() // empty: Match returns nil → processPoint returns after cache update

	// Start one normal worker and one high-priority worker.
	e.wg.Add(2)
	go e.processingLoop()
	go e.processingLoopHighPriority()

	// Give workers time to start.
	time.Sleep(20 * time.Millisecond)

	// Saturate the main channel with many normal-priority points.
	// The single normal worker will be busy draining these.
	for i := 0; i < 500; i++ {
		e.dataBus.Push(core.DataPoint{
			Driver:    "plc",
			Tag:       "bulk",
			Value:     float64(i),
			Timestamp: time.Now(),
		})
	}

	// Push a high-priority point. The dedicated high-priority worker should
	// process it promptly even while the normal worker is still draining
	// the 500 bulk points.
	hpTag := "urgent"
	e.dataBus.PushHighPriority(core.DataPoint{
		Driver:    "plc",
		Tag:       hpTag,
		Value:     999.0,
		Timestamp: time.Now(),
	})

	// The high-priority point should appear in the cache quickly (within
	// 200ms), proving it was processed by the dedicated worker without
	// waiting for the 500 normal points to drain.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := e.cache.Get("plc", hpTag); ok {
			return // Success — high-priority data processed despite saturation.
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("high-priority point was not processed within 200ms despite dedicated worker")
}

// TestScheduleDriverTagsSetsPriority verifies that scheduleDriverTags
// classifies fast-interval tasks (≤ 1s) as high-priority and slow-interval
// tasks as normal priority (IMPROVEMENTS #5).
func TestScheduleDriverTagsSetsPriority(t *testing.T) {
	e := New().(*CoreCEngine)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	defer e.cancel()
	e.scheduler = NewScheduler(
		func(ctx context.Context, driver string, tags []string) ([]core.TagValue, error) {
			return nil, nil
		},
		func(driver string, values []core.TagValue, priority int) {},
		0,
		statistic.NewManager(),
	)
	if err := e.scheduler.Start(e.ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	defer e.scheduler.Stop()

	// Fast interval (200ms) → high priority (priority=1)
	// Slow interval (5s) → normal priority (priority=0)
	e.scheduleDriverTags(core.DriverConfig{
		Name: "plc",
		Type: "mock",
		Tags: []core.TagConfig{
			{Name: "fast", Address: "1", Type: "float32", Interval: "200ms"},
			{Name: "slow", Address: "2", Type: "float32", Interval: "5s"},
		},
	})

	tasks := e.scheduler.tasks
	var fastPriority, slowPriority int
	fastFound, slowFound := false, false
	for _, tr := range tasks {
		if tr.task.Driver == "plc" {
			if tr.task.Interval == 200*time.Millisecond {
				fastPriority = tr.task.Priority
				fastFound = true
			}
			if tr.task.Interval == 5*time.Second {
				slowPriority = tr.task.Priority
				slowFound = true
			}
		}
	}
	if !fastFound {
		t.Fatal("fast-interval task not found")
	}
	if !slowFound {
		t.Fatal("slow-interval task not found")
	}
	if fastPriority != 1 {
		t.Errorf("fast-interval (200ms) priority = %d, want 1 (high)", fastPriority)
	}
	if slowPriority != 0 {
		t.Errorf("slow-interval (5s) priority = %d, want 0 (normal)", slowPriority)
	}
}

// compile-time guard: ensure the test helpers don't drift.
var _ = sync.WaitGroup{}
