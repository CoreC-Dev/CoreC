package s7

// ============================================================
// Behavior tests for merged area reads.
//
// The merged reader changes three observable behaviours:
//  1. one failed merged read marks every tag in that segment QualityBad
//     (not per-tag failures on separate reads);
//  2. tags whose gap exceeds s7MergeGapLimit are not merged, so sparse
//     batches keep an intermediate round-trip count;
//  3. bit tags are still read one by one.
//
// The tests below pin those behaviours plus the core invariant: the
// simulated client's trip count equals
// the merged batch count.
// ============================================================

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// errSimSegmentRejected is the failure-injection sentinel for the
// simulated PLC: it contains no connection-lost keyword, so the driver
// must treat it as a data error, not a connection error.
var errSimSegmentRejected = errors.New("sim plc: segment rejected")

// simImageBytes returns the bytes the simulated PLC serves for
// [start, start+n) of any DB image.
func simImageBytes(start, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = simByteAt(start + i)
	}
	return out
}

func expectSimUint8(start int) uint8 { return simByteAt(start) }

func expectSimUint16(start int) uint16 {
	return binary.BigEndian.Uint16(simImageBytes(start, 2))
}

func expectSimUint32(start int) uint32 {
	return binary.BigEndian.Uint32(simImageBytes(start, 4))
}

// expectSimFloat32 mirrors gos7's big-endian Real decoding independently.
func expectSimFloat32(start int) float32 {
	return math.Float32frombits(binary.BigEndian.Uint32(simImageBytes(start, 4)))
}

// expectSimBit mirrors gos7's GetBoolAt bit test independently.
func expectSimBit(byteOffset, bit int) bool {
	return simByteAt(byteOffset)&(1<<uint(bit)) != 0
}

// newSimScenarioDriver builds a connected driver over the simulated PLC
// image and returns it together with the sim client.
func newSimScenarioDriver(t *testing.T, name string, tags []core.TagConfig) (*S7Driver, *simS7Client) {
	t.Helper()
	sim := &simS7Client{images: map[string][]byte{}}
	return initSimDriver(t, name, tags, sim), sim
}

// namesOf returns the tag names of the configs in order.
func namesOf(tags []core.TagConfig) []string {
	names := make([]string, len(tags))
	for i, tag := range tags {
		names[i] = tag.Name
	}
	return names
}

// readAllGood performs a Read and fails the test unless every tag comes
// back QualityGood, in input order.
func readAllGood(t *testing.T, d *S7Driver, names []string) []core.TagValue {
	t.Helper()
	vals, err := d.Read(context.Background(), names)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(vals) != len(names) {
		t.Fatalf("got %d values, want %d", len(vals), len(names))
	}
	for i, v := range vals {
		if v.Tag != names[i] {
			t.Errorf("result %d is tag %q, want %q (input order)", i, v.Tag, names[i])
		}
		if v.Quality != core.QualityGood {
			t.Errorf("tag %s quality = %v, err = %v", v.Tag, v.Quality, v.Error)
		}
	}
	return vals
}

// TestS7ReadMergedRoundTrips asserts the core acceptance criterion on
// the full scenario matrix: the simulated client's trip count
// equals the projected merged batch count (chunked at s7MaxBatchSize,
// merged per chunk) — never one per tag.
func TestS7ReadMergedRoundTrips(t *testing.T) {
	for _, scen := range benchScenarios() {
		t.Run(scen.name, func(t *testing.T) {
			d, sim := newSimScenarioDriver(t, scen.name, scen.tags)
			readAllGood(t, d, scen.names)
			if got, want := sim.tripCount(), projectedScenarioTrips(scen); got != want {
				t.Errorf("round trips = %d, want %d (merged batch count)", got, want)
			}
		})
	}
}

// TestS7ReadContiguousWordsMergeIntoOneTrip reads three contiguous DB
// words — in shuffled input order — and expects exactly one PLC round trip
// covering bytes [0, 6), with results returned in input order.
func TestS7ReadContiguousWordsMergeIntoOneTrip(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "w0", Address: "DB1.DBW0", Type: "uint16"},
		{Name: "w2", Address: "DB1.DBW2", Type: "uint16"},
		{Name: "w4", Address: "DB1.DBW4", Type: "uint16"},
	}
	d, sim := newSimScenarioDriver(t, "contig-words", tags)

	vals := readAllGood(t, d, []string{"w4", "w0", "w2"}) // shuffled input order

	if got := sim.tripCount(); got != 1 {
		t.Errorf("round trips = %d, want 1", got)
	}
	wantCall := simReadCall{area: areaDB, dbNumber: 1, start: 0, size: 6}
	calls := sim.recordedCalls()
	if len(calls) != 1 || calls[0] != wantCall {
		t.Errorf("read calls = %+v, want [%+v]", calls, wantCall)
	}
	wantVals := map[string]uint16{"w0": expectSimUint16(0), "w2": expectSimUint16(2), "w4": expectSimUint16(4)}
	for i, name := range []string{"w4", "w0", "w2"} {
		if got := vals[i].Value.(uint16); got != wantVals[name] {
			t.Errorf("tag %s value = %d, want %d", name, got, wantVals[name])
		}
	}
}

// TestS7ReadSparseGapLimitBoundary pins the gap limit: a 32-byte gap
// merges into one read, a 33-byte gap stays two reads.
func TestS7ReadSparseGapLimitBoundary(t *testing.T) {
	tests := []struct {
		name      string
		addresses []string
		wantTrips int
	}{
		{"gap of 32 bytes merges", []string{"DB1.DBD0", "DB1.DBD36"}, 1},
		{"gap of 33 bytes stays separate", []string{"DB1.DBD0", "DB1.DBD37"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := make([]core.TagConfig, len(tt.addresses))
			for i, addr := range tt.addresses {
				tags[i] = core.TagConfig{Name: fmt.Sprintf("t%d", i), Address: addr, Type: "float32"}
			}
			d, sim := newSimScenarioDriver(t, tt.name, tags)
			readAllGood(t, d, namesOf(tags))
			if got := sim.tripCount(); got != tt.wantTrips {
				t.Errorf("round trips = %d, want %d", got, tt.wantTrips)
			}
		})
	}
}

// TestS7ReadBitTagsStayPerTag pins registered behaviour 3: bit tags are
// never merged — every bit costs its own round trip, next to the one
// round trip the contiguous word segment needs.
func TestS7ReadBitTagsStayPerTag(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "b0", Address: "DB1.DBX0.0", Type: "bool"},
		{Name: "b3", Address: "DB1.DBX0.3", Type: "bool"},
		{Name: "b8", Address: "DB1.DBX8.7", Type: "bool"},
		{Name: "w", Address: "DB1.DBW0", Type: "uint16"},
	}
	d, sim := newSimScenarioDriver(t, "bits", tags)

	names := namesOf(tags)
	vals := readAllGood(t, d, names)

	if got, want := sim.tripCount(), 4; got != want {
		t.Errorf("round trips = %d, want %d (one word segment + one per bit)", got, want)
	}
	wantBits := map[string]bool{
		"b0": expectSimBit(0, 0),
		"b3": expectSimBit(0, 3),
		"b8": expectSimBit(8, 7),
	}
	for _, v := range vals {
		switch name := v.Tag; name {
		case "b0", "b3", "b8":
			if got := v.Value.(bool); got != wantBits[name] {
				t.Errorf("bit %s value = %v, want %v", name, got, wantBits[name])
			}
		case "w":
			if got := v.Value.(uint16); got != expectSimUint16(0) {
				t.Errorf("word value = %d, want %d", got, expectSimUint16(0))
			}
		}
	}
}

// TestS7ReadCrossDBAndAreaDoNotMerge pins the grouping key: tags in
// different DBs or memory areas never share a read. Groups are read in
// ascending (area, dbNumber) order, independent of input order.
func TestS7ReadCrossDBAndAreaDoNotMerge(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "db2", Address: "DB2.DBW0", Type: "uint16"},
		{Name: "db1", Address: "DB1.DBW0", Type: "uint16"},
		{Name: "mk", Address: "MW0", Type: "uint16"},
	}
	d, sim := newSimScenarioDriver(t, "cross-groups", tags)

	readAllGood(t, d, namesOf(tags))

	if got, want := sim.tripCount(), 3; got != want {
		t.Fatalf("round trips = %d, want %d", got, want)
	}
	wantCalls := []simReadCall{
		{area: areaDB, dbNumber: 1, start: 0, size: 2},
		{area: areaDB, dbNumber: 2, start: 0, size: 2},
		{area: areaM, dbNumber: 0, start: 0, size: 2},
	}
	calls := sim.recordedCalls()
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want)
		}
	}
}

// TestS7ReadOverlappingTagsMerge verifies that overlapping tags (a byte
// inside a word) merge into a single read and both decode correctly from
// the shared buffer.
func TestS7ReadOverlappingTagsMerge(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "byte", Address: "DB1.DBB1", Type: "uint8"},
		{Name: "word", Address: "DB1.DBW0", Type: "uint16"},
	}
	d, sim := newSimScenarioDriver(t, "overlap", tags)

	vals := readAllGood(t, d, namesOf(tags))

	if got := sim.tripCount(); got != 1 {
		t.Errorf("round trips = %d, want 1", got)
	}
	wantCall := simReadCall{area: areaDB, dbNumber: 1, start: 0, size: 2}
	if calls := sim.recordedCalls(); len(calls) != 1 || calls[0] != wantCall {
		t.Errorf("read calls = %+v, want [%+v]", calls, wantCall)
	}
	if got := vals[0].Value.(uint8); got != expectSimUint8(1) {
		t.Errorf("byte value = %d, want %d", got, expectSimUint8(1))
	}
	if got := vals[1].Value.(uint16); got != expectSimUint16(0) {
		t.Errorf("word value = %d, want %d", got, expectSimUint16(0))
	}
}

// TestS7ReadSegmentCapSplitsContiguousRun verifies the 480-byte segment
// cap: 121 contiguous dwords (484 bytes) split into a 480-byte read and
// a 4-byte read.
func TestS7ReadSegmentCapSplitsContiguousRun(t *testing.T) {
	const n = 121
	tags := make([]core.TagConfig, n)
	for i := range tags {
		tags[i] = core.TagConfig{
			Name:    fmt.Sprintf("d%03d", i),
			Address: fmt.Sprintf("DB1.DBD%d", i*4),
			Type:    "float32",
		}
	}
	d, sim := newSimScenarioDriver(t, "cap-split", tags)

	readAllGood(t, d, namesOf(tags))

	if got, want := sim.tripCount(), 2; got != want {
		t.Fatalf("round trips = %d, want %d", got, want)
	}
	wantCalls := []simReadCall{
		{area: areaDB, dbNumber: 1, start: 0, size: s7MaxPDUReadLen},
		{area: areaDB, dbNumber: 1, start: s7MaxPDUReadLen, size: 4},
	}
	calls := sim.recordedCalls()
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want)
		}
	}
}

// TestS7ReadBatchChunkingPreservesMerging verifies the outer batch
// contract is untouched: a 230-tag Read is chunked at s7MaxBatchSize
// (220) and each chunk merges its own contiguous run, so the read count
// still records 230 tags and the round trips stay at the merged count.
func TestS7ReadBatchChunkingPreservesMerging(t *testing.T) {
	const n = 230
	tags := make([]core.TagConfig, n)
	for i := range tags {
		tags[i] = core.TagConfig{
			Name:    fmt.Sprintf("w%03d", i),
			Address: fmt.Sprintf("DB1.DBW%d", i*2),
			Type:    "uint16",
		}
	}
	d, sim := newSimScenarioDriver(t, "chunking", tags)

	readAllGood(t, d, namesOf(tags))

	// Chunk 1: 220 tags × 2 B = 440 B → one segment.
	// Chunk 2: 10 tags × 2 B = 20 B → one segment.
	if got, want := sim.tripCount(), 2; got != want {
		t.Errorf("round trips = %d, want %d (one per batch chunk)", got, want)
	}
	if got, want := d.ReadCount(), uint64(n); got != want {
		t.Errorf("read count = %d, want %d", got, want)
	}
}

// TestS7ReadUnknownTagCostsNoTraffic verifies unknown tags fail per-tag
// without any PLC traffic, unchanged from the tag-per-trip reader.
func TestS7ReadUnknownTagCostsNoTraffic(t *testing.T) {
	tags := []core.TagConfig{{Name: "known", Address: "DB1.DBW0", Type: "uint16"}}
	d, sim := newSimScenarioDriver(t, "unknown", tags)

	vals, err := d.Read(context.Background(), []string{"ghost", "known"})
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("got %d values, want 2", len(vals))
	}
	if vals[0].Quality != core.QualityBad || vals[0].Error == nil {
		t.Errorf("unknown tag result = %+v, want QualityBad with error", vals[0])
	}
	if vals[1].Quality != core.QualityGood {
		t.Errorf("known tag quality = %v, want good: %v", vals[1].Quality, vals[1].Error)
	}
	if got := sim.tripCount(); got != 1 {
		t.Errorf("round trips = %d, want 1 (unknown tags cost no traffic)", got)
	}
}

// TestS7ReadSegmentFailureFailsWholeSegment pins registered behaviour 1:
// when a merged read fails, every tag in that segment is QualityBad with
// the same error, tags in other segments still succeed, and the error
// counter records one failed round trip — not one per tag.
func TestS7ReadSegmentFailureFailsWholeSegment(t *testing.T) {
	var tags []core.TagConfig
	for db := 1; db <= 3; db++ {
		for w := 0; w < 3; w++ {
			tags = append(tags, core.TagConfig{
				Name:    fmt.Sprintf("db%d_w%d", db, w),
				Address: fmt.Sprintf("DB%d.DBW%d", db, w*2),
				Type:    "uint16",
			})
		}
	}
	sim := &simS7Client{
		images: map[string][]byte{},
		fail: func(c simReadCall) error {
			if c.area == areaDB && c.dbNumber == 2 {
				return errSimSegmentRejected
			}
			return nil
		},
	}
	d := initSimDriver(t, "segment-fail", tags, sim)
	names := namesOf(tags)

	beforeErr := d.ErrorCount()
	vals, err := d.Read(context.Background(), names)
	if err != nil {
		t.Fatalf("Read must not return a top-level error for per-segment failures: %v", err)
	}
	if len(vals) != len(names) {
		t.Fatalf("got %d values, want %d", len(vals), len(names))
	}
	for _, v := range vals {
		if v.Quality != core.QualityBad && v.Quality != core.QualityGood {
			t.Fatalf("unexpected quality %v for %s", v.Quality, v.Tag)
		}
		wantBad := len(v.Tag) > 3 && v.Tag[:3] == "db2"
		if wantBad {
			if v.Quality != core.QualityBad || v.Error == nil {
				t.Errorf("tag %s = %+v, want QualityBad (whole segment fails)", v.Tag, v)
			} else if v.Error.Error() != "s7 read error: "+errSimSegmentRejected.Error() {
				t.Errorf("tag %s error = %q, want wrapped %q", v.Tag, v.Error, "s7 read error: "+errSimSegmentRejected.Error())
			}
			continue
		}
		if v.Quality != core.QualityGood {
			t.Errorf("tag %s in a healthy segment failed: %v", v.Tag, v.Error)
		}
	}
	if got, want := sim.tripCount(), 3; got != want {
		t.Errorf("round trips = %d, want %d (one per DB)", got, want)
	}
	// One failed round trip is one recorded error, not three.
	if got := d.ErrorCount(); got != beforeErr+1 {
		t.Errorf("error count = %d, want %d (one per failed segment)", got, beforeErr+1)
	}
	if d.Status().LastError == "" {
		t.Error("LastError should record the segment failure")
	}
	// A non-connection error must not tear the connection down.
	if got := d.GetState(); got != core.StateConnected {
		t.Errorf("state = %v, want StateConnected for a data error", got)
	}
}

// TestS7ReadBitFailureIsolatedPerTag verifies the unmerged bit path keeps
// the old per-tag failure semantics: one failing bit read marks only that
// bit, not its neighbours.
func TestS7ReadBitFailureIsolatedPerTag(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "ok_bit", Address: "DB1.DBX0.0", Type: "bool"},
		{Name: "bad_bit", Address: "DB1.DBX8.0", Type: "bool"},
	}
	sim := &simS7Client{
		images: map[string][]byte{},
		fail: func(c simReadCall) error {
			if c.area == areaDB && c.start == 8 {
				return errSimSegmentRejected
			}
			return nil
		},
	}
	d := initSimDriver(t, "bit-fail", tags, sim)

	vals, err := d.Read(context.Background(), namesOf(tags))
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if vals[0].Quality != core.QualityGood {
		t.Errorf("healthy bit quality = %v, want good", vals[0].Quality)
	}
	if vals[1].Quality != core.QualityBad || vals[1].Error == nil {
		t.Errorf("poisoned bit result = %+v, want QualityBad with error", vals[1])
	}
	if got, want := sim.tripCount(), 2; got != want {
		t.Errorf("round trips = %d, want %d (one per bit)", got, want)
	}
}

// TestS7ReadSegmentConnectionErrorTearsDownConnection verifies a
// connection-class failure inside a merged segment still triggers the
// reconnect teardown through the stale-client guard.
func TestS7ReadSegmentConnectionErrorTearsDownConnection(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "merge-conn-lost",
		Type: "s7",
		Settings: map[string]any{
			"host":               "127.0.0.1",
			"port":               1, // dead loopback port: instant refusal
			"reconnect-interval": "1h",
		},
		Tags: []core.TagConfig{
			{Name: "w0", Address: "DB1.DBW0", Type: "uint16"},
			{Name: "w2", Address: "DB1.DBW2", Type: "uint16"},
		},
	}
	drv, err := NewS7Driver(cfg)
	if err != nil {
		t.Fatalf("NewS7Driver: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Start fails to connect (dead port) but sets the lifecycle context
	// so the reconnect loop launched by the teardown is valid.
	if err := drv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := drv.Stop(); err != nil {
			t.Errorf("cleanup Stop: %v", err)
		}
	})
	d := drv.(*S7Driver)

	sim := &simS7Client{
		images: map[string][]byte{},
		fail:   func(simReadCall) error { return errors.New("read tcp 10.0.0.2:102: connection reset by peer") },
	}
	d.Lock()
	d.client = sim
	d.SetStateLocked(core.StateConnected)
	d.Unlock()

	vals, err := d.Read(context.Background(), []string{"w0", "w2"})
	if err != nil {
		t.Fatalf("Read must not return a top-level error: %v", err)
	}
	for _, v := range vals {
		if v.Quality != core.QualityBad {
			t.Errorf("tag %s quality = %v, want bad", v.Tag, v.Quality)
		}
	}
	if got := d.GetState(); got != core.StateError {
		t.Errorf("state = %v, want StateError (connection loss must trigger teardown)", got)
	}
}

// TestS7ReadMergedValuesMatchImage is the offset-slicing correctness
// check: a randomized mixed batch (word tags of several types across DBs
// and the Merker area, plus bit tags, plus scaled tags) must decode every
// value exactly as the PLC image dictates, while the trip count equals
// the projection plus one per bit.
func TestS7ReadMergedValuesMatchImage(t *testing.T) {
	r := rand.New(rand.NewSource(7)) //nolint:gosec // deterministic test data, not cryptography
	var tags []core.TagConfig
	var wordSpans []s7Span
	const bits = 12

	// Word tags cycle over four types and four (area, DB) groups, with
	// random 0-40 byte gaps between consecutive tags of a group.
	typeDefs := []struct {
		typeName string
		dbKind   string // DB address kind suffix
		mKind    string // Merker address kind suffix
		size     int
	}{
		{"uint8", "DBB", "MB", 1},
		{"uint16", "DBW", "MW", 2},
		{"uint32", "DBD", "MD", 4},
		{"float32", "DBD", "MD", 4},
	}
	offsets := map[string]int{"DB1": 0, "DB2": 0, "DB3": 0, "M": 0}
	wordStarts := map[string]int{}
	for i := 0; i < 60; i++ {
		td := typeDefs[i%len(typeDefs)]
		group := []string{"DB1", "DB2", "DB3", "M"}[i%4]
		start := offsets[group]
		wordStarts[fmt.Sprintf("word%02d", i)] = start

		var addr string
		if group == "M" {
			addr = fmt.Sprintf("%s%d", td.mKind, start) // mKind is "MB"/"MW"/"MD"
			wordSpans = append(wordSpans, s7Span{area: areaM, start: start, size: td.size})
		} else {
			dbNum := int(group[2] - '0')
			addr = fmt.Sprintf("DB%d.%s%d", dbNum, td.dbKind, start)
			wordSpans = append(wordSpans, s7Span{area: areaDB, dbNumber: dbNum, start: start, size: td.size})
		}
		cfg := core.TagConfig{Name: fmt.Sprintf("word%02d", i), Address: addr, Type: td.typeName}
		if i%7 == 3 {
			cfg.Scale, cfg.Offset = 2, 1 // transform must survive merging
		}
		tags = append(tags, cfg)
		offsets[group] = start + td.size + r.Intn(41)
	}
	// Bit tags in DB4 (their own group, read one by one).
	for i := 0; i < bits; i++ {
		tags = append(tags, core.TagConfig{
			Name:    fmt.Sprintf("bit%02d", i),
			Address: fmt.Sprintf("DB4.DBX%d.%d", i*3, i%8),
			Type:    "bool",
		})
	}

	d, sim := newSimScenarioDriver(t, "values-match", tags)
	names := namesOf(tags)
	vals := readAllGood(t, d, names)

	for _, v := range vals {
		switch {
		case len(v.Tag) > 4 && v.Tag[:4] == "word":
			i := mustTestAtoi(t, v.Tag[4:])
			start := wordStarts[v.Tag]
			checkValue(t, v, expectedWordValue(i, typeDefs[i%len(typeDefs)].typeName, start))
		case len(v.Tag) > 3 && v.Tag[:3] == "bit":
			i := mustTestAtoi(t, v.Tag[3:])
			checkValue(t, v, expectSimBit(i*3, i%8))
		default:
			t.Errorf("unexpected tag %q in results", v.Tag)
		}
	}

	wantTrips := projectedSegments(wordSpans) + bits
	if got := sim.tripCount(); got != wantTrips {
		t.Errorf("round trips = %d, want %d (merged word segments + one per bit)", got, wantTrips)
	}
}

// expectedWordValue decodes the value the PLC image holds at start for
// the tag's type, applying the transform the configuration asks for
// (tags with index ≡ 3 mod 7 carry scale 2 / offset 1, mirroring
// util.ApplyTransform's per-type behaviour — every numeric width is
// transformed; uint8 included since the ApplyTransform type-coverage
// fix, with scaled integers decoding as float64).
func expectedWordValue(i int, typeName string, start int) any {
	scaled := i%7 == 3
	switch typeName {
	case "uint8":
		v := expectSimUint8(start)
		if scaled {
			return float64(v)*2 + 1
		}
		return v
	case "uint16":
		v := expectSimUint16(start)
		if scaled {
			return float64(v)*2 + 1
		}
		return v
	case "uint32":
		v := expectSimUint32(start)
		if scaled {
			return float64(v)*2 + 1
		}
		return v
	default: // float32
		v := expectSimFloat32(start)
		if scaled {
			return float32(float64(v)*2 + 1)
		}
		return v
	}
}

// checkValue compares a decoded tag value against the expected Go value
// with exact type matching, so a wrong slice offset or a lost transform
// fails loudly.
func checkValue(t *testing.T, v core.TagValue, want any) {
	t.Helper()
	switch want := want.(type) {
	case uint8:
		if got, ok := v.Value.(uint8); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (uint8)", v.Tag, v.Value, v.Value, want)
		}
	case uint16:
		if got, ok := v.Value.(uint16); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (uint16)", v.Tag, v.Value, v.Value, want)
		}
	case uint32:
		if got, ok := v.Value.(uint32); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (uint32)", v.Tag, v.Value, v.Value, want)
		}
	case float32:
		if got, ok := v.Value.(float32); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (float32)", v.Tag, v.Value, v.Value, want)
		}
	case float64:
		if got, ok := v.Value.(float64); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (float64)", v.Tag, v.Value, v.Value, want)
		}
	case bool:
		if got, ok := v.Value.(bool); !ok || got != want {
			t.Errorf("tag %s value = %v (%T), want %v (bool)", v.Tag, v.Value, v.Value, want)
		}
	default:
		t.Errorf("tag %s: unhandled expected type %T", v.Tag, want)
	}
}

func mustTestAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("bad numeric tag suffix %q: %v", s, err)
	}
	return n
}

// TestS7PlanMatchesProjection diff-tests the production planner against
// the independent projection transcribed from the design document, over
// the full benchmark matrix and the planner's boundary cases.
func TestS7PlanMatchesProjection(t *testing.T) {
	var allSpans []s7Span
	for _, scen := range benchScenarios() {
		allSpans = append(allSpans, scen.spans...)
	}
	allSpans = append(allSpans,
		s7Span{area: areaDB, dbNumber: 1, start: 0, size: 4},
		s7Span{area: areaDB, dbNumber: 1, start: 36, size: 4},
		s7Span{area: areaDB, dbNumber: 1, start: 37, size: 4},
		s7Span{area: areaM, start: 0, size: 2},
		s7Span{area: areaQ, start: 4, size: 4},
	)

	tags := make([]s7ReadTag, len(allSpans))
	for i, sp := range allSpans {
		tags[i] = s7ReadTag{index: i, addr: s7Address{
			area: sp.area, dbNumber: sp.dbNumber, start: sp.start, size: sp.size,
		}}
	}
	segments := planS7ReadSegments(tags)
	if got, want := len(segments), projectedSegments(allSpans); got != want {
		t.Errorf("planS7ReadSegments produced %d segments, projection says %d", got, want)
	}

	// Shape check on the cap boundary: 121 contiguous dwords split into
	// a full 480-byte segment and a 4-byte tail.
	capTags := make([]s7ReadTag, 121)
	for i := range capTags {
		capTags[i] = s7ReadTag{index: i, addr: s7Address{area: areaDB, dbNumber: 1, start: i * 4, size: 4}}
	}
	segs := planS7ReadSegments(capTags)
	if len(segs) != 2 {
		t.Fatalf("got %d segments for the cap case, want 2", len(segs))
	}
	if segs[0].start != 0 || segs[0].length != s7MaxPDUReadLen {
		t.Errorf("first segment = [%d, %d), want [0, %d)", segs[0].start, segs[0].start+segs[0].length, s7MaxPDUReadLen)
	}
	if segs[1].start != s7MaxPDUReadLen || segs[1].length != 4 {
		t.Errorf("second segment = [%d, %d), want [%d, %d)", segs[1].start, segs[1].start+segs[1].length, s7MaxPDUReadLen, s7MaxPDUReadLen+4)
	}
	if len(segs[0].entries) != 120 || len(segs[1].entries) != 1 {
		t.Errorf("segment entry counts = %d, %d; want 120, 1", len(segs[0].entries), len(segs[1].entries))
	}
}
