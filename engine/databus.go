// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"sync"
	"sync/atomic"

	"github.com/CoreC-Dev/CoreC/core"
)

// DataBus is the internal data channel that connects drivers to the processing pipeline.
// It uses Go channels for high-performance, lock-free data flow.
//
// DataBus supports two priority tiers (IMPROVEMENTS #5): a main channel for
// normal-priority data and a dedicated high-priority channel for fast-interval
// tasks. The engine runs separate worker goroutines for each tier so a burst
// of low-frequency bulk reads cannot starve high-frequency collection.
type DataBus struct {
	ch          chan core.DataPoint
	highPriCh   chan core.DataPoint
	subscribers []subscriber
	mu          sync.RWMutex
	closed      bool

	// subCount tracks the number of active subscribers so Broadcast can
	// take a fast path (no lock) when there is nobody to deliver to. It is
	// maintained atomically alongside the subscribers slice under b.mu.
	subCount atomic.Int64

	// dropped counts messages silently dropped because a subscriber's
	// buffer was full (slow subscriber). Read via Dropped().
	dropped atomic.Int64

	// pushDropped counts messages dropped from the main channel because
	// the bus was full and Push had to evict the oldest entry.
	pushDropped atomic.Int64

	// highPriPushDropped counts messages dropped from the high-priority
	// channel because its buffer was full. Tracked separately so operators
	// can distinguish backpressure on the priority path.
	highPriPushDropped atomic.Int64
}

type subscriber struct {
	filter string
	ch     chan core.DataPoint
}

// NewDataBus creates a new DataBus with the given buffer size.
// The high-priority channel uses a smaller buffer (1/4 of the main, capped
// at 1024) because high-frequency data is less voluminous per tick.
func NewDataBus(bufferSize int) *DataBus {
	if bufferSize <= 0 {
		bufferSize = 4096
	}
	highPriSize := bufferSize / 4
	if highPriSize > 1024 {
		highPriSize = 1024
	}
	if highPriSize < 64 {
		highPriSize = 64
	}
	return &DataBus{
		ch:        make(chan core.DataPoint, bufferSize),
		highPriCh: make(chan core.DataPoint, highPriSize),
	}
}

// Push sends a DataPoint into the bus.
// It is safe to call Push concurrently with Close; a Push that
// races with Close is silently dropped instead of panicking.
func (b *DataBus) Push(point core.DataPoint) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	b.mu.RUnlock()

	defer func() {
		if r := recover(); r != nil {
			// The only expected panic is "send on closed channel" from a race
			// between Push and Close. Any other panic indicates a real bug and
			// should not be silently swallowed.
			b.mu.RLock()
			closed := b.closed
			b.mu.RUnlock()
			if !closed {
				panic(r) // re-panic for unexpected panics
			}
		}
	}()
	select {
	case b.ch <- point:
	default:
		// Bus is full, drop oldest to make room for newest
		select {
		case <-b.ch:
			b.pushDropped.Add(1)
		default:
		}
		// Try again non-blocking; if still full (another goroutine filled
		// the slot between drain and send), drop the new point rather
		// than blocking forever.
		select {
		case b.ch <- point:
		default:
			b.pushDropped.Add(1)
		}
	}
}

// Channel returns the main data channel for consumption.
func (b *DataBus) Channel() <-chan core.DataPoint {
	return b.ch
}

// PushHighPriority sends a DataPoint into the high-priority channel.
// It uses the same drop-oldest semantics as Push so the newest
// high-frequency data is always retained. Safe to call concurrently with
// Close (IMPROVEMENTS #5: priority-tiered data routing).
func (b *DataBus) PushHighPriority(point core.DataPoint) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	b.mu.RUnlock()

	defer func() {
		if r := recover(); r != nil {
			b.mu.RLock()
			closed := b.closed
			b.mu.RUnlock()
			if !closed {
				panic(r)
			}
		}
	}()
	select {
	case b.highPriCh <- point:
	default:
		select {
		case <-b.highPriCh:
			b.highPriPushDropped.Add(1)
		default:
		}
		select {
		case b.highPriCh <- point:
		default:
			b.highPriPushDropped.Add(1)
		}
	}
}

// HighPriorityChannel returns the high-priority data channel for consumption
// by dedicated high-priority workers.
func (b *DataBus) HighPriorityChannel() <-chan core.DataPoint {
	return b.highPriCh
}

// DefaultSubscriberBuffer is the default buffer size for DataBus subscriber channels.
const DefaultSubscriberBuffer = 256

// Subscribe creates a new subscriber channel.
func (b *DataBus) Subscribe(filter string) (events <-chan core.DataPoint, unsub func()) {
	return b.SubscribeWithBuffer(filter, DefaultSubscriberBuffer)
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size. size <=0 falls back to DefaultSubscriberBuffer.
func (b *DataBus) SubscribeWithBuffer(filter string, size int) (events <-chan core.DataPoint, unsub func()) {
	if size <= 0 {
		size = DefaultSubscriberBuffer
	}
	ch := make(chan core.DataPoint, size)
	b.mu.Lock()
	b.subscribers = append(b.subscribers, subscriber{filter: filter, ch: ch})
	b.subCount.Add(1)
	b.mu.Unlock()

	unsub = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, sub := range b.subscribers {
			if sub.ch == ch {
				if !b.closed {
					close(sub.ch)
				}
				b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
				b.subCount.Add(-1)
				break
			}
		}
	}
	return ch, unsub
}

// Broadcast sends a DataPoint to all subscribers.
// Subscribers with a non-empty filter only receive points whose Driver
// matches the filter; an empty filter receives all points.
func (b *DataBus) Broadcast(point core.DataPoint) {
	// Fast path: no subscribers, skip the lock entirely. This runs on
	// every DataPoint, so avoiding the RLock when nobody is listening is
	// a meaningful win. subCount is maintained atomically alongside the
	// subscribers slice under b.mu.
	if b.subCount.Load() == 0 {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subscribers {
		// Apply filter: empty filter = all data, non-empty = matching driver only.
		if sub.filter != "" && point.Driver != sub.filter {
			continue
		}
		select {
		case sub.ch <- point:
		default:
			// Subscriber is slow, skip
			b.dropped.Add(1)
		}
	}
}

// Dropped returns the total number of messages dropped due to slow subscribers.
func (b *DataBus) Dropped() int64 {
	return b.dropped.Load()
}

// PushDropped returns the number of messages evicted from the main channel
// because the bus buffer was full (producer faster than consumer).
func (b *DataBus) PushDropped() int64 {
	return b.pushDropped.Load()
}

// HighPriorityPushDropped returns the number of messages evicted from the
// high-priority channel because its buffer was full.
func (b *DataBus) HighPriorityPushDropped() int64 {
	return b.highPriPushDropped.Load()
}

// Close closes the data bus.
func (b *DataBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		close(b.ch)
		close(b.highPriCh)
		for _, sub := range b.subscribers {
			close(sub.ch)
		}
		// Clear the subscriber list and reset subCount so that a
		// Broadcast racing with Close (or called after Close) cannot
		// reach the send select on a closed channel. Without this,
		// subCount would remain > 0 and Broadcast would enter the loop
		// and panic on `select { case sub.ch <- point: default: }`.
		b.subscribers = nil
		b.subCount.Store(0)
		b.closed = true
	}
}
