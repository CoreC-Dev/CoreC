// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"sync"
	"sync/atomic"

	"github.com/CoreC-Dev/CoreC/core"
)

// DataBus is the internal data channel that connects drivers to the processing pipeline.
// It uses Go channels for high-performance, lock-free data flow.
type DataBus struct {
	ch          chan core.DataPoint
	subscribers []subscriber
	mu          sync.RWMutex
	closed      bool

	// dropped counts messages silently dropped because a subscriber's
	// buffer was full (slow subscriber). Read via Dropped().
	dropped atomic.Int64

	// pushDropped counts messages dropped from the main channel because
	// the bus was full and Push had to evict the oldest entry.
	pushDropped atomic.Int64
}

type subscriber struct {
	filter string
	ch     chan core.DataPoint
}

// NewDataBus creates a new DataBus with the given buffer size.
func NewDataBus(bufferSize int) *DataBus {
	if bufferSize <= 0 {
		bufferSize = 4096
	}
	return &DataBus{
		ch: make(chan core.DataPoint, bufferSize),
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

// Close closes the data bus.
func (b *DataBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		close(b.ch)
		for _, sub := range b.subscribers {
			close(sub.ch)
		}
		b.closed = true
	}
}
