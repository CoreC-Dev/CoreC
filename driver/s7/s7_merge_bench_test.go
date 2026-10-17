package s7

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/robinson/gos7"
)

// ============================================================
// Simulated PLC client, scenario builders, merge projection and
// benchmarks for PERF-003 (S7 per-tag N+1 reads).
//
// The design (docs/design-docs, PERF-003) requires proving the cost of the
// current tag-per-trip Read with numbers before touching production code.
// Everything in this file is test-only: the simulated client counts PLC
// round trips against a deterministic in-memory image, the scenarios are
// the design's 100/500-tag × 3-distribution matrix, and the projection
// computes the round-trip count the merged-read algorithm would need.
// ============================================================

// simImageSize is the byte length of each simulated memory-area image. The
// sparse 500-tag scenario spans ~18 KB, so 32 KB leaves headroom.
const simImageSize = 32 * 1024

// simByteAt returns the deterministic byte stored at an image offset. All
// bytes are < 0x80 so big-endian float32/float64 values decoded from the
// image are never NaN or Inf (the exponent can never reach 0xFF), keeping
// value comparisons in tests meaningful.
func simByteAt(offset int) byte {
	return byte(((offset&0x7F)*89 + (offset>>7)*29) % 128)
}

// simReadCall records one simulated area read.
type simReadCall struct {
	area     s7Area
	dbNumber int
	start    int
	size     int
}

// simS7Client is a gos7.Client double backed by a deterministic in-memory
// PLC image. Every area read counts as one simulated PLC round trip and can
// be given per-read latency, so benchmarks measure both the number of
// round trips a Read needs and its wall-clock cost.
//
// Only the four area-read methods the driver's Read path uses are
// implemented. The embedded nil gos7.Client satisfies the rest of the
// interface at compile time; calling any of those methods panics on the nil
// interface, so an unexpected driver call surfaces immediately in a test
// instead of silently succeeding.
type simS7Client struct {
	gos7.Client // embedded nil interface: unimplemented methods panic

	mu     sync.Mutex
	delay  time.Duration
	images map[string][]byte
	trips  int
	calls  []simReadCall

	// fail, when non-nil, is consulted on every read; a non-nil error is
	// returned to the driver as the read result (failure injection).
	fail func(call simReadCall) error
}

func (s *simS7Client) AGReadDB(dbNumber, start, size int, buffer []byte) error {
	return s.read(areaDB, dbNumber, start, size, buffer)
}

func (s *simS7Client) AGReadMB(start, size int, buffer []byte) error {
	return s.read(areaM, 0, start, size, buffer)
}

func (s *simS7Client) AGReadEB(start, size int, buffer []byte) error {
	return s.read(areaI, 0, start, size, buffer)
}

func (s *simS7Client) AGReadAB(start, size int, buffer []byte) error {
	return s.read(areaQ, 0, start, size, buffer)
}

// Compile-time assertion that simS7Client satisfies gos7.Client.
var _ gos7.Client = (*simS7Client)(nil)

// read serves one area read from the image, after the configured delay,
// counting it as one round trip.
func (s *simS7Client) read(area s7Area, dbNumber, start, size int, buffer []byte) error {
	call := simReadCall{area: area, dbNumber: dbNumber, start: start, size: size}

	s.mu.Lock()
	s.trips++
	s.calls = append(s.calls, call)
	delay := s.delay
	fail := s.fail
	img := s.imageLocked(area, dbNumber)
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay) // simulated PLC round-trip latency
	}
	if fail != nil {
		if err := fail(call); err != nil {
			return err
		}
	}
	copyFromImage(img, start, buffer)
	return nil
}

// imageLocked lazily creates the image for one (area, dbNumber). Callers
// must hold s.mu. Images are write-once, so serving reads outside the lock
// is safe.
func (s *simS7Client) imageLocked(area s7Area, dbNumber int) []byte {
	key := fmt.Sprintf("%d:%d", area, dbNumber)
	img, ok := s.images[key]
	if !ok {
		img = make([]byte, simImageSize)
		for i := range img {
			img[i] = simByteAt(i)
		}
		s.images[key] = img
	}
	return img
}

// copyFromImage copies img[start : start+len(buffer)] into buffer,
// zero-filling offsets beyond the image so out-of-range reads still
// succeed deterministically.
func copyFromImage(img []byte, start int, buffer []byte) {
	for i := range buffer {
		if off := start + i; off >= 0 && off < len(img) {
			buffer[i] = img[off]
		}
	}
}

func (s *simS7Client) tripCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trips
}

// recordedCalls returns a copy of the read-call history.
func (s *simS7Client) recordedCalls() []simReadCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]simReadCall(nil), s.calls...)
}

// ============================================================
// Scenario builders: the PERF-003 design matrix.
//
// 100/500 float32 tags in three address distributions:
//   - contig:   one DB, consecutive dword offsets (no gaps)
//   - sparse:   one DB, random 0-64 byte gaps between consecutive tags
//   - multi-db: round-robin across DB1-DB3, contiguous within each DB
// ============================================================

// s7Span is a bare address span: the input of the merge projection.
type s7Span struct {
	area     s7Area
	dbNumber int
	start    int
	size     int
}

// benchScenario is one address-distribution scenario.
type benchScenario struct {
	name  string
	tags  []core.TagConfig
	names []string
	spans []s7Span
}

// benchScenarios returns the full 100/500 × contig/sparse/multi-db matrix.
func benchScenarios() []benchScenario {
	var out []benchScenario
	for _, n := range []int{100, 500} {
		out = append(out,
			contigScenario(n),
			sparseScenario(n),
			multiDBScenario(n),
		)
	}
	return out
}

// addTag appends one float32 tag at the given DB byte offset.
func (s *benchScenario) addTag(i, db, start int) {
	name := fmt.Sprintf("tag%04d", i)
	s.tags = append(s.tags, core.TagConfig{
		Name:    name,
		Address: fmt.Sprintf("DB%d.DBD%d", db, start),
		Type:    "float32",
	})
	s.names = append(s.names, name)
	s.spans = append(s.spans, s7Span{area: areaDB, dbNumber: db, start: start, size: 4})
}

// contigScenario: n tags in DB1 at consecutive dword offsets (0, 4, 8, ...).
func contigScenario(n int) benchScenario {
	s := benchScenario{name: fmt.Sprintf("tags=%d/dist=contig", n)}
	for i := 0; i < n; i++ {
		s.addTag(i, 1, i*4)
	}
	return s
}

// sparseScenario: n tags in DB1 with a random 0-64 byte gap between one
// tag's end and the next tag's start. The fixed seed makes the scenario
// (and its projected trip count) reproducible.
func sparseScenario(n int) benchScenario {
	s := benchScenario{name: fmt.Sprintf("tags=%d/dist=sparse", n)}
	r := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test data, not cryptography
	start := 0
	for i := 0; i < n; i++ {
		s.addTag(i, 1, start)
		start += 4 + r.Intn(65)
	}
	return s
}

// multiDBScenario: n tags round-robin across DB1-DB3, contiguous within
// each DB — three merge groups instead of one.
func multiDBScenario(n int) benchScenario {
	s := benchScenario{name: fmt.Sprintf("tags=%d/dist=multi-db", n)}
	offsets := [3]int{}
	for i := 0; i < n; i++ {
		db := 1 + i%3
		s.addTag(i, db, offsets[db-1])
		offsets[db-1] += 4
	}
	return s
}

// initSimDriver builds a driver via the public constructor, initializes it
// with the given tags, then swaps in the simulated client and marks it
// connected so Read runs fully in-process.
func initSimDriver(tb testing.TB, name string, tags []core.TagConfig, client gos7.Client) *S7Driver {
	tb.Helper()
	drv, err := NewS7Driver(core.DriverConfig{Name: name, Type: TypeName})
	if err != nil {
		tb.Fatalf("NewS7Driver: %v", err)
	}
	d, ok := drv.(*S7Driver)
	if !ok {
		tb.Fatalf("NewS7Driver returned %T, want *S7Driver", drv)
	}
	cfg := core.DriverConfig{
		Name:     name,
		Type:     TypeName,
		Settings: map[string]any{"host": "127.0.0.1"},
		Tags:     tags,
	}
	if err := d.Init(context.Background(), cfg); err != nil {
		tb.Fatalf("Init: %v", err)
	}
	d.Lock()
	d.client = client
	d.SetStateLocked(core.StateConnected)
	d.Unlock()
	return d
}

// ============================================================
// Merge projection (design algorithm, transcribed independently).
// ============================================================

// projectedSegments computes how many PLC round trips the merged-read
// algorithm needs for the given spans: group by (area, dbNumber), sort each
// group by start offset, then greedily merge neighbours whose byte gap is
// at most 32, capping every merged segment at 480 bytes.
//
// The limits and the algorithm are transcribed from the PERF-003 design
// document. They are deliberately NOT shared with the production planner,
// so the two implementations can be checked against each other
// (TestS7PlanMatchesProjection) and a transcription slip on either side
// shows up as a mismatch.
func projectedSegments(spans []s7Span) int {
	type groupKey struct {
		area s7Area
		db   int
	}
	groups := make(map[groupKey][]s7Span)
	for _, sp := range spans {
		k := groupKey{sp.area, sp.dbNumber}
		groups[k] = append(groups[k], sp)
	}

	total := 0
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].start < g[j].start })
		segStart, segLen := 0, 0
		for _, sp := range g {
			if segLen == 0 || sp.start-(segStart+segLen) > 32 || sp.start+sp.size-segStart > 480 {
				total++ // gap or length limit exceeded: start a new segment
				segStart, segLen = sp.start, sp.size
				continue
			}
			if end := sp.start + sp.size; end-segStart > segLen {
				segLen = end - segStart // overlapping span extends the segment
			}
		}
	}
	return total
}

// TestS7ProjectedSegments checks the projection against hand-computed
// segment counts for the merge algorithm's boundary cases.
func TestS7ProjectedSegments(t *testing.T) {
	db := func(start, size int) s7Span { return s7Span{area: areaDB, dbNumber: 1, start: start, size: size} }

	tests := []struct {
		name  string
		spans []s7Span
		want  int
	}{
		{"single span", []s7Span{db(0, 4)}, 1},
		{"three contiguous dwords merge", []s7Span{db(0, 4), db(4, 4), db(8, 4)}, 1},
		{"gap of exactly 32 merges", []s7Span{db(0, 4), db(36, 4)}, 1},
		{"gap of 33 stays separate", []s7Span{db(0, 4), db(37, 4)}, 2},
		{"overlapping spans merge", []s7Span{db(0, 2), db(1, 1)}, 1},
		{"contained span merges", []s7Span{db(0, 4), db(1, 2)}, 1},
		{"different DBs never merge", []s7Span{db(0, 4), {area: areaDB, dbNumber: 2, start: 0, size: 4}}, 2},
		{"different areas never merge", []s7Span{db(0, 4), {area: areaM, start: 0, size: 4}}, 2},
		{"480-byte span fits one segment", contigSpans(120), 1},
		{"484-byte span splits at the cap", contigSpans(121), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectedSegments(tt.spans); got != tt.want {
				t.Errorf("projectedSegments(%v) = %d segments, want %d", tt.spans, got, tt.want)
			}
		})
	}
}

// contigSpans returns n contiguous dword spans starting at offset 0.
// 120 spans cover exactly 480 bytes (the segment cap).
func contigSpans(n int) []s7Span {
	spans := make([]s7Span, n)
	for i := range spans {
		spans[i] = s7Span{area: areaDB, dbNumber: 1, start: i * 4, size: 4}
	}
	return spans
}

// projectedScenarioTrips computes the round trips the merged reader needs
// for a whole scenario: the driver first chunks the tag list at
// s7MaxBatchSize (its advertised batch contract) and merges within each
// chunk, so the projection must chunk the spans the same way.
func projectedScenarioTrips(scen benchScenario) int {
	total := 0
	for start := 0; start < len(scen.spans); start += s7MaxBatchSize {
		end := start + s7MaxBatchSize
		if end > len(scen.spans) {
			end = len(scen.spans)
		}
		total += projectedSegments(scen.spans[start:end])
	}
	return total
}

// TestS7ReadScenarioCoverageInvariants verifies the scenario matrix feeds
// the driver exactly what the projection assumes: every scenario tag
// resolves to the span recorded alongside it.
func TestS7ReadScenarioCoverageInvariants(t *testing.T) {
	for _, scen := range benchScenarios() {
		if len(scen.tags) != len(scen.spans) || len(scen.names) != len(scen.spans) {
			t.Errorf("%s: %d tags, %d names, %d spans — must match", scen.name, len(scen.tags), len(scen.names), len(scen.spans))
		}
		for i, cfg := range scen.tags {
			dt, ok := core.ParseDataType(cfg.Type)
			if !ok || dt != core.TypeFloat32 {
				t.Errorf("%s: tag %s type = %q, want float32", scen.name, cfg.Name, cfg.Type)
			}
			addr, err := parseS7Address(cfg.Address, dt)
			if err != nil {
				t.Errorf("%s: tag %s address %q: %v", scen.name, cfg.Name, cfg.Address, err)
				continue
			}
			want := scen.spans[i]
			if addr.area != want.area || addr.dbNumber != want.dbNumber ||
				addr.start != want.start || addr.size != want.size {
				t.Errorf("%s: tag %s parsed %+v, span says %+v", scen.name, cfg.Name, addr, want)
			}
		}
	}
}

// BenchmarkS7ReadTripCost measures Read on the full PERF-003 scenario
// matrix at simulated PLC latencies of 0/1/5/10 ms per round trip. The
// custom metrics report the measured round trips per Read, the merge
// projection's round trips, and the ratio between them. With the merged
// implementation the measured count must equal the projection (ratio
// 1.0); a higher ratio means the merge regressed toward tag-per-trip
// reads. Baseline numbers for the old tag-per-trip implementation (one
// AGReadDB per tag) are recorded in the PERF-003 tracker entry.
func BenchmarkS7ReadTripCost(b *testing.B) {
	for _, scen := range benchScenarios() {
		for _, delay := range []time.Duration{0, time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond} {
			b.Run(fmt.Sprintf("%s/delay=%s", scen.name, delay), func(b *testing.B) {
				sim := &simS7Client{images: map[string][]byte{}, delay: delay}
				d := initSimDriver(b, scen.name, scen.tags, sim)
				names := scen.names
				ctx := context.Background()

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					vals, err := d.Read(ctx, names)
					if err != nil {
						b.Fatal(err)
					}
					if len(vals) != len(names) {
						b.Fatalf("got %d values, want %d", len(vals), len(names))
					}
					for _, v := range vals {
						if v.Quality != core.QualityGood {
							b.Fatalf("tag %s quality = %v, err = %v", v.Tag, v.Quality, v.Error)
						}
					}
				}
				b.StopTimer()

				trips := float64(sim.tripCount()) / float64(b.N)
				projected := float64(projectedScenarioTrips(scen))
				b.ReportMetric(trips, "trips/read")
				b.ReportMetric(projected, "projected-trips/read")
				b.ReportMetric(trips/projected, "collapse-x")
			})
		}
	}
}
