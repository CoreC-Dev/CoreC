package opcua

import (
	"context"
	"expvar"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua/stats"
	"github.com/gopcua/opcua/ua"
)

// makeReadValues builds n distinct ReadValueIDs for chunking tests.
func makeReadValues(n int) []*ua.ReadValueID {
	vals := make([]*ua.ReadValueID, n)
	for i := range vals {
		vals[i] = &ua.ReadValueID{
			NodeID:       ua.NewNumericNodeID(2, uint32(i+1)),
			AttributeID:  ua.AttributeIDValue,
			DataEncoding: &ua.QualifiedName{},
		}
	}
	return vals
}

// TestChunkReadValues verifies the chunking used by Read: chunk sizes,
// node order across chunks, the single-chunk fallbacks for size >= len
// and non-positive size, and empty input yielding no chunks.
func TestChunkReadValues(t *testing.T) {
	tests := []struct {
		name      string
		n         int
		size      int
		wantSizes []int
	}{
		{"5 nodes size 2", 5, 2, []int{2, 2, 1}},
		{"4 nodes size 2 exact split", 4, 2, []int{2, 2}},
		{"5 nodes size 1", 5, 1, []int{1, 1, 1, 1, 1}},
		{"size equal to length", 5, 5, []int{5}},
		{"size greater than length", 3, 10, []int{3}},
		{"single node size 1", 1, 1, []int{1}},
		{"size zero yields single chunk", 5, 0, []int{5}},
		{"negative size yields single chunk", 5, -2, []int{5}},
		{"empty input", 0, 2, nil},
		{"empty input size zero", 0, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vals := makeReadValues(tt.n)
			chunks := chunkReadValues(vals, tt.size)
			if len(chunks) != len(tt.wantSizes) {
				t.Fatalf("chunk count = %d, want %d", len(chunks), len(tt.wantSizes))
			}
			total := 0
			for i, chunk := range chunks {
				if len(chunk) != tt.wantSizes[i] {
					t.Errorf("chunk[%d] size = %d, want %d", i, len(chunk), tt.wantSizes[i])
				}
				for j, rv := range chunk {
					if rv != vals[total+j] {
						t.Errorf("chunk[%d][%d] is not the original node at position %d", i, j, total+j)
					}
				}
				total += len(chunk)
			}
			if total != tt.n {
				t.Errorf("total nodes across chunks = %d, want %d", total, tt.n)
			}
		})
	}
}

// chunkReadConfig returns a config with max-batch-size 2 and five tags,
// so a five-tag Read spans three chunks.
func chunkReadConfig(name string) core.DriverConfig {
	cfg := validConfig(name)
	cfg.Settings["max-batch-size"] = 2
	cfg.Tags = nil
	for i := 1; i <= 5; i++ {
		cfg.Tags = append(cfg.Tags, core.TagConfig{
			Name:    fmt.Sprintf("t%d", i),
			Address: fmt.Sprintf("ns=2;s=Node%d", i),
			Type:    "float64",
		})
	}
	return cfg
}

var chunkReadTags = []string{"t1", "t2", "t3", "t4", "t5"}

// TestOPCUAReadChunkedError verifies the chunked Read error path: with
// max-batch-size 2 and five tags, the unconnected client fails on the
// first chunk and Read returns the wrapped batch error with nil values.
// The error counter advances once, the read counter and LastRead stay
// untouched, and LastError holds the raw underlying error.
func TestOPCUAReadChunkedError(t *testing.T) {
	d := initConnectedOPCUA(t, chunkReadConfig("chunk-read"))
	if d.maxBatchSize != 2 {
		t.Fatalf("maxBatchSize = %d, want 2", d.maxBatchSize)
	}

	beforeErr := d.ErrorCount()
	beforeRead := d.ReadCount()
	vals, err := d.Read(context.Background(), chunkReadTags)
	if err == nil {
		t.Fatal("expected error from unconnected client, got nil")
	}
	if vals != nil {
		t.Errorf("expected nil values on chunk failure, got %v", vals)
	}
	if !strings.Contains(err.Error(), "opcua batch read failed") {
		t.Errorf("error = %q, want substring 'opcua batch read failed'", err.Error())
	}
	if d.ErrorCount() != beforeErr+1 {
		t.Errorf("errorCount = %d, want %d", d.ErrorCount(), beforeErr+1)
	}
	if d.ReadCount() != beforeRead {
		t.Errorf("readCount = %d, want %d (counters advance only after all chunks succeed)", d.ReadCount(), beforeRead)
	}
	st := d.Status()
	if !st.LastRead.IsZero() {
		t.Errorf("LastRead = %v, want zero after a failed chunked read", st.LastRead)
	}
	if !strings.Contains(st.LastError, "not connected") {
		t.Errorf("LastError = %q, want substring 'not connected'", st.LastError)
	}
}

// expvarCounter reads an integer counter from an expvar map, treating a
// missing key as zero.
func expvarCounter(t *testing.T, m *expvar.Map, key string) int64 {
	t.Helper()
	v := m.Get(key)
	if v == nil {
		return 0
	}
	n, err := strconv.ParseInt(v.String(), 10, 64)
	if err != nil {
		t.Fatalf("stat %q = %q is not an integer: %v", key, v.String(), err)
	}
	return n
}

// TestOPCUAReadChunkRequestSize proves the request-size cap through the
// real client call using gopcua's client statistics: with five tags and
// max-batch-size 2, the failed Read issues exactly one client Read call
// carrying 2 nodes (the first chunk), not all 5. A non-positive
// max-batch-size sends all nodes in a single request.
func TestOPCUAReadChunkRequestSize(t *testing.T) {
	t.Run("first chunk capped at max-batch-size", func(t *testing.T) {
		d := initConnectedOPCUA(t, chunkReadConfig("chunk-size"))

		readsBefore := expvarCounter(t, stats.Client(), "Read")
		nodesBefore := expvarCounter(t, stats.Client(), "NodesToRead")

		_, err := d.Read(context.Background(), chunkReadTags)
		if err == nil {
			t.Fatal("expected error from unconnected client, got nil")
		}

		if got := expvarCounter(t, stats.Client(), "Read") - readsBefore; got != 1 {
			t.Errorf("client Read calls = %d, want 1 (the read stops at the failed first chunk)", got)
		}
		if got := expvarCounter(t, stats.Client(), "NodesToRead") - nodesBefore; got != 2 {
			t.Errorf("NodesToRead sent = %d, want 2 (first chunk capped at max-batch-size)", got)
		}
	})

	t.Run("non-positive max-batch-size sends one request", func(t *testing.T) {
		cfg := chunkReadConfig("chunk-size-zero")
		cfg.Settings["max-batch-size"] = 0
		d := initConnectedOPCUA(t, cfg)
		if d.maxBatchSize != 0 {
			t.Fatalf("maxBatchSize = %d, want 0 (GetIntSetting passes explicit zero through)", d.maxBatchSize)
		}

		readsBefore := expvarCounter(t, stats.Client(), "Read")
		nodesBefore := expvarCounter(t, stats.Client(), "NodesToRead")

		_, err := d.Read(context.Background(), chunkReadTags)
		if err == nil {
			t.Fatal("expected error from unconnected client, got nil")
		}

		if got := expvarCounter(t, stats.Client(), "Read") - readsBefore; got != 1 {
			t.Errorf("client Read calls = %d, want 1", got)
		}
		if got := expvarCounter(t, stats.Client(), "NodesToRead") - nodesBefore; got != 5 {
			t.Errorf("NodesToRead sent = %d, want 5 (single request without chunking)", got)
		}
	})
}
