package route

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// These tests exercise the offline-buffer and flush-batch-drop metric
// emission paths in metrics_engine.go. The default mockEngineV2 does not
// satisfy core.OfflineBufferStatsProvider or core.FlushBatchesDroppedProvider,
// so those families are omitted — these tests cover the emission branches by
// using a mock that DOES satisfy the optional role interfaces. No real engine
// or HTTP backend is required beyond httptest.

// mockOfflineBufferEngine extends mockEngineV2 with the offline-buffer and
// flush-batch-drop role interfaces so writeOfflineBufferMetrics and
// writeFlushBatchesDropped emit their families.
type mockOfflineBufferEngine struct {
	mockEngineV2
	pending      int
	drained      uint64
	pushed       uint64
	flushDropped uint64
}

func (m *mockOfflineBufferEngine) OfflineBufferStats() (pending int, drained, pushed uint64) {
	return m.pending, m.drained, m.pushed
}

func (m *mockOfflineBufferEngine) FlushBatchesDropped() uint64 {
	return m.flushDropped
}

// ─── writeOfflineBufferMetrics (direct) ────────────────────────────

// TestWriteOfflineBufferMetricsDirect verifies that when the engine satisfies
// OfflineBufferStatsProvider, writeOfflineBufferMetrics emits the pending
// gauge and the drained/pushed counters with the exact values and HELP/TYPE
// headers.
func TestWriteOfflineBufferMetricsDirect(t *testing.T) {
	eng := &mockOfflineBufferEngine{
		pending: 3,
		drained: 10,
		pushed:  2,
	}
	SetEngine(eng)

	var b strings.Builder
	writeOfflineBufferMetrics(&b)
	out := b.String()

	checks := []string{
		"# HELP corec_offline_buffer_pending",
		"# TYPE corec_offline_buffer_pending gauge",
		"corec_offline_buffer_pending 3",
		"# HELP corec_offline_buffer_drained_total",
		"# TYPE corec_offline_buffer_drained_total counter",
		"corec_offline_buffer_drained_total 10",
		"# HELP corec_offline_buffer_pushed_total",
		"# TYPE corec_offline_buffer_pushed_total counter",
		"corec_offline_buffer_pushed_total 2",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("offline buffer metrics missing %q\nOutput:\n%s", s, out)
		}
	}
}

// TestWriteOfflineBufferMetricsAbsentWhenNotProvider verifies that when the
// engine does NOT satisfy OfflineBufferStatsProvider, no offline-buffer
// metrics are emitted (graceful omission).
func TestWriteOfflineBufferMetricsAbsentWhenNotProvider(t *testing.T) {
	SetEngine(&mockEngineV2{})

	var b strings.Builder
	writeOfflineBufferMetrics(&b)
	out := b.String()

	if out != "" {
		t.Errorf("expected no offline buffer metrics when engine lacks the provider interface, got:\n%s", out)
	}
}

// TestWriteOfflineBufferMetricsZero verifies that a provider reporting all
// zeros still emits the families with zero values (the emission path is not
// gated on non-zero values).
func TestWriteOfflineBufferMetricsZero(t *testing.T) {
	eng := &mockOfflineBufferEngine{}
	SetEngine(eng)

	var b strings.Builder
	writeOfflineBufferMetrics(&b)
	out := b.String()

	for _, s := range []string{
		"corec_offline_buffer_pending 0",
		"corec_offline_buffer_drained_total 0",
		"corec_offline_buffer_pushed_total 0",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("zero-value offline buffer metrics missing %q\nOutput:\n%s", s, out)
		}
	}
}

// ─── writeFlushBatchesDropped (direct) ─────────────────────────────

// TestWriteFlushBatchesDroppedDirect verifies that when the engine satisfies
// FlushBatchesDroppedProvider, writeFlushBatchesDropped emits the counter with
// the exact value and HELP/TYPE headers.
func TestWriteFlushBatchesDroppedDirect(t *testing.T) {
	eng := &mockOfflineBufferEngine{flushDropped: 7}
	SetEngine(eng)

	var b strings.Builder
	writeFlushBatchesDropped(&b)
	out := b.String()

	checks := []string{
		"# HELP corec_flush_batches_dropped_total",
		"# TYPE corec_flush_batches_dropped_total counter",
		"corec_flush_batches_dropped_total 7",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("flush batches dropped metrics missing %q\nOutput:\n%s", s, out)
		}
	}
}

// TestWriteFlushBatchesDroppedAbsentWhenNotProvider verifies graceful omission
// when the engine does not satisfy FlushBatchesDroppedProvider.
func TestWriteFlushBatchesDroppedAbsentWhenNotProvider(t *testing.T) {
	SetEngine(&mockEngineV2{})

	var b strings.Builder
	writeFlushBatchesDropped(&b)
	out := b.String()

	if out != "" {
		t.Errorf("expected no flush-batches-dropped metrics when engine lacks the provider interface, got:\n%s", out)
	}
}

// ─── /metrics endpoint integration ─────────────────────────────────

// TestPromMetricsOfflineBuffer verifies that the offline-buffer and
// flush-batches-dropped families appear in the /metrics scrape output with the
// correct values when the engine satisfies the optional role interfaces.
func TestPromMetricsOfflineBuffer(t *testing.T) {
	eng := &mockOfflineBufferEngine{
		mockEngineV2: mockEngineV2{
			stats: core.EngineStats{
				TotalRead:    100,
				TotalPublish: 90,
				Drivers:      1,
				Transports:   1,
			},
		},
		pending:      5,
		drained:      20,
		pushed:       4,
		flushDropped: 9,
	}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := authedGet(t, ts, "/metrics", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	out := string(body)

	checks := []string{
		"# HELP corec_offline_buffer_pending",
		"# TYPE corec_offline_buffer_pending gauge",
		"corec_offline_buffer_pending 5",
		"# HELP corec_offline_buffer_drained_total",
		"# TYPE corec_offline_buffer_drained_total counter",
		"corec_offline_buffer_drained_total 20",
		"# HELP corec_offline_buffer_pushed_total",
		"# TYPE corec_offline_buffer_pushed_total counter",
		"corec_offline_buffer_pushed_total 4",
		"# HELP corec_flush_batches_dropped_total",
		"# TYPE corec_flush_batches_dropped_total counter",
		"corec_flush_batches_dropped_total 9",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("metrics output missing %q\n", s)
		}
	}
}
