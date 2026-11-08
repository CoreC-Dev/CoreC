package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// partialBatchTransport fails a chosen index on the first PublishBatch and
// succeeds afterwards, recording the tag names of every attempt.
type partialBatchTransport struct {
	mu        sync.Mutex
	calls     [][]string
	failFirst bool
}

func (t *partialBatchTransport) Init(context.Context, core.TransportConfig) error { return nil }
func (t *partialBatchTransport) Start(context.Context) error                      { return nil }
func (t *partialBatchTransport) Stop() error                                      { return nil }
func (t *partialBatchTransport) Publish(context.Context, core.DataPoint) error    { return nil }
func (t *partialBatchTransport) PublishBatch(_ context.Context, points []core.DataPoint) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	tags := make([]string, len(points))
	for i := range points {
		tags[i] = points[i].Tag
	}
	t.calls = append(t.calls, tags)
	if t.failFirst {
		t.failFirst = false
		return &core.BatchPublishError{Failed: []int{1}, Err: errors.New("point 2 failed")}
	}
	return nil
}
func (t *partialBatchTransport) OnCommand() <-chan core.WriteCommand { return nil }
func (t *partialBatchTransport) OnData() <-chan core.DataPoint       { return nil }
func (t *partialBatchTransport) Name() string                        { return "partial" }
func (t *partialBatchTransport) Type() string                        { return "partial" }
func (t *partialBatchTransport) Status() core.TransportStatus        { return core.TransportStatus{} }

// TestBatchRetryResendsOnlyFailedPoints is the P1-4 regression: a partial
// failure must retry only the failed indexes; resending the whole batch
// duplicates the points that already reached the broker.
func TestBatchRetryResendsOnlyFailedPoints(t *testing.T) {
	tr := &partialBatchTransport{failFirst: true}
	published := 0
	b := newTransportBatcher(
		tr,
		core.TransportConfig{Name: "partial", RetryCount: 2},
		nil, nil, nil,
		func(n int) { published += n },
		time.Second,
	)
	if b == nil {
		t.Fatal("batcher was not constructed")
	}

	ok := b.retry.publishWithRetryAndBuffer(context.Background(), []core.DataPoint{
		{Tag: "a"}, {Tag: "b"},
	})
	if !ok {
		t.Fatal("batch should succeed after retrying the failed point")
	}

	tr.mu.Lock()
	calls := append([][]string(nil), tr.calls...)
	tr.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("publish attempts = %d, want 2: %v", len(calls), calls)
	}
	if len(calls[0]) != 2 || calls[0][0] != "a" || calls[0][1] != "b" {
		t.Fatalf("first attempt = %v, want [a b]", calls[0])
	}
	if len(calls[1]) != 1 || calls[1][0] != "b" {
		t.Fatalf("retry = %v, want only the failed point [b]", calls[1])
	}
	if published != 2 {
		t.Fatalf("published count = %d, want 2 (no double count, no dropped point)", published)
	}
}
