package observable

import (
	"sync"
	"sync/atomic"
)

// Observable is a generic fan-out event bus.
// It reads from a source channel and fans out to multiple subscribers.
type Observable[T any] struct {
	source <-chan T
	subs   map[chan<- T]struct{}
	mu     sync.Mutex

	// dropped counts messages silently dropped because a subscriber's
	// buffer was full (slow subscriber). Read via Dropped().
	dropped atomic.Int64
}

// NewObservable creates a new Observable and starts the fan-out goroutine.
func NewObservable[T any](source <-chan T) *Observable[T] {
	o := &Observable[T]{
		source: source,
		subs:   make(map[chan<- T]struct{}),
	}
	go o.start()
	return o
}

func (o *Observable[T]) start() {
	for v := range o.source {
		o.mu.Lock()
		for sub := range o.subs {
			select {
			case sub <- v:
			default:
				// slow subscriber, drop message to prevent backpressure
				o.dropped.Add(1)
			}
		}
		o.mu.Unlock()
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	for sub := range o.subs {
		close(sub)
	}
}

// Dropped returns the total number of messages dropped due to slow subscribers.
func (o *Observable[T]) Dropped() int64 {
	return o.dropped.Load()
}

// DefaultSubscriberBuffer is the default buffer size for subscriber channels.
const DefaultSubscriberBuffer = 128

// Subscribe returns a channel of events and an unsubscribe function.
func (o *Observable[T]) Subscribe() (events <-chan T, unsub func()) {
	return o.SubscribeWithBuffer(DefaultSubscriberBuffer)
}

// SubscribeWithBuffer is like Subscribe but lets the caller choose the
// subscriber channel buffer size. size <=0 falls back to DefaultSubscriberBuffer.
func (o *Observable[T]) SubscribeWithBuffer(size int) (events <-chan T, unsub func()) {
	if size <= 0 {
		size = DefaultSubscriberBuffer
	}
	ch := make(chan T, size)

	o.mu.Lock()
	o.subs[ch] = struct{}{}
	o.mu.Unlock()

	unsub = func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if _, ok := o.subs[ch]; ok {
			delete(o.subs, ch)
			close(ch)
		}
	}

	return ch, unsub
}
