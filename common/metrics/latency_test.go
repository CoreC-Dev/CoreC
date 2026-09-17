package metrics

import (
	"sync"
	"testing"
	"time"
)

// buckets returns the default read/publish bucket set, which the tests
// below rely on as a known-sorted scale.
func testBuckets() []float64 {
	return []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}
}

func TestNewLatencyHistogramCopiesBuckets(t *testing.T) {
	in := testBuckets()
	h := NewLatencyHistogram(in)
	// Mutate the caller's slice; the histogram must be unaffected.
	in[0] = 999
	got, _, _, _ := h.Snapshot()
	if got[0] == 999 {
		t.Fatalf("NewLatencyHistogram did not copy buckets: got %v", got)
	}
	if len(got) != len(testBuckets()) {
		t.Fatalf("expected %d buckets, got %d", len(testBuckets()), len(got))
	}
}

func TestNewLatencyHistogramEmptyBuckets(t *testing.T) {
	// A nil/empty bucket set must still record sum and count.
	h := NewLatencyHistogram(nil)
	h.Observe(2 * time.Millisecond)
	_, counts, sum, count := h.Snapshot()
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
	if sum <= 0 {
		t.Fatalf("expected positive sum, got %v", sum)
	}
	if len(counts) != 0 {
		t.Fatalf("expected no bucket counts, got %v", counts)
	}
}

func TestObserveBucketsCorrectly(t *testing.T) {
	h := NewLatencyHistogram(testBuckets())

	// Observations chosen to land in distinct buckets.
	//   0.5ms  -> <= 0.001  (bucket 0)
	//   3ms    -> <= 0.005  (bucket 1)
	//   20ms   -> <= 0.05   (bucket 3)
	//   15s    -> exceeds all buckets (+Inf)
	h.Observe(500 * time.Microsecond)
	h.Observe(3 * time.Millisecond)
	h.Observe(20 * time.Millisecond)
	h.Observe(15 * time.Second)

	buckets, counts, sum, count := h.Snapshot()

	if count != 4 {
		t.Fatalf("expected count 4, got %d", count)
	}

	// Cumulative expectation: bucket i holds the number of observations
	// whose duration is <= buckets[i].
	want := []uint64{1, 2, 2, 3, 3, 3, 3, 3, 3}
	for i := range buckets {
		if counts[i] != want[i] {
			t.Errorf("bucket le=%v: expected cumulative count %d, got %d", buckets[i], want[i], counts[i])
		}
	}

	// sum = 0.0005 + 0.003 + 0.02 + 15 = 15.0235 (allow float epsilon).
	wantSum := 0.0005 + 0.003 + 0.02 + 15.0
	if diff := sum - wantSum; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("expected sum ~%v, got %v", wantSum, sum)
	}
}

func TestObserveExactlyOnBucketBound(t *testing.T) {
	h := NewLatencyHistogram(testBuckets())
	// An observation exactly equal to a bucket upper bound must count
	// in that bucket (inclusive <= comparison).
	h.Observe(5 * time.Millisecond) // exactly 0.005s -> bucket 1 (le=0.005)
	_, counts, _, count := h.Snapshot()
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
	// bucket 1 and all above should be 1; bucket 0 should be 0.
	for i, c := range counts {
		if i < 1 {
			if c != 0 {
				t.Errorf("bucket %d (below bound): expected 0, got %d", i, c)
			}
		} else if c != 1 {
			t.Errorf("bucket %d (at/above bound): expected 1, got %d", i, c)
		}
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	h := NewLatencyHistogram(testBuckets())
	h.Observe(2 * time.Millisecond)

	buckets, counts, _, _ := h.Snapshot()
	// Mutate the returned copies; a subsequent Snapshot must be unaffected.
	buckets[0] = -1
	counts[0] = 1<<63 - 1

	buckets2, counts2, _, _ := h.Snapshot()
	if buckets2[0] == -1 || counts2[0] == 1<<63-1 {
		t.Fatalf("Snapshot did not return an independent copy")
	}
}

func TestSnapshotAccumulatesAcrossObservations(t *testing.T) {
	h := NewLatencyHistogram(testBuckets())
	for i := 0; i < 100; i++ {
		h.Observe(time.Millisecond)
	}
	_, counts, _, count := h.Snapshot()
	if count != 100 {
		t.Fatalf("expected count 100, got %d", count)
	}
	// 1ms <= 0.001 (bucket 0), so every bucket must be 100.
	for i, c := range counts {
		if c != 100 {
			t.Errorf("bucket %d: expected 100, got %d", i, c)
		}
	}
}

func TestObserveConcurrent(t *testing.T) {
	// Race-free under -race: many goroutines observe concurrently and
	// the final count must match the number of observations. No fixed
	// time.Sleep is used to align goroutines; we only join on the
	// WaitGroup after every goroutine has returned.
	h := NewLatencyHistogram(testBuckets())

	const goroutines = 16
	const perGoroutine = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perGoroutine; i++ {
				h.Observe(time.Microsecond)
			}
		}()
	}
	close(start)
	wg.Wait()

	_, counts, _, count := h.Snapshot()
	want := uint64(goroutines * perGoroutine)
	if count != want {
		t.Fatalf("expected count %d, got %d", want, count)
	}
	// 1us <= 0.001, so every bucket must equal the total count.
	for i, c := range counts {
		if c != want {
			t.Errorf("bucket %d: expected %d, got %d", i, want, c)
		}
	}
}

func TestDefaultBucketsMatchSpec(t *testing.T) {
	want := []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}
	if !equal(DefaultReadLatencyBuckets, want) {
		t.Errorf("DefaultReadLatencyBuckets = %v, want %v", DefaultReadLatencyBuckets, want)
	}
	if !equal(DefaultPublishLatencyBuckets, want) {
		t.Errorf("DefaultPublishLatencyBuckets = %v, want %v", DefaultPublishLatencyBuckets, want)
	}
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
