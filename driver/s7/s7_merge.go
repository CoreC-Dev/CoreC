package s7

// ============================================================
// Merged area reads (PERF-003).
//
// The tag-per-trip reader issued one AGRead* call per tag, so a Read of N
// tags cost N serial PLC round trips even when every tag lived in the same
// DB a few bytes apart (see s7_merge_bench_test.go for the measured cost).
// The planner below groups tags by (area, dbNumber), sorts them by start
// offset and greedily merges neighbours into single area reads, collapsing
// the round-trip count from N to the number of merged segments.
// ============================================================

import (
	"fmt"
	"sort"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/robinson/gos7"
)

const (
	// s7MergeGapLimit is the maximum inter-tag gap, in bytes, that still
	// merges two tags into one read. Reading up to 32 bytes of padding
	// between two tags is cheaper than a second PLC round trip; a larger
	// gap falls back to separate reads.
	s7MergeGapLimit = 32

	// s7MaxPDUReadLen caps the byte length of one merged segment. S7-400/
	// 1200/1500 CPUs negotiate a 480-byte PDU, so a 480-byte read is a
	// single telegram on those models. S7-200/300 negotiate 240 bytes;
	// gos7 then splits the read into several telegrams inside the same
	// AGRead* call, which is still far cheaper than one call per tag.
	s7MaxPDUReadLen = 480
)

// s7ReadTag is one resolved tag request within a Read batch.
type s7ReadTag struct {
	index int // position of the tag in the Read call (its result slot)
	name  string
	addr  s7Address
	dt    core.DataType

	// Transform parameters (the only TagConfig fields the read path uses).
	// Keeping them instead of the whole TagConfig keeps the struct small,
	// since grouping and sorting copy tags by value.
	scale  float64
	offset float64
}

// s7ReadSegment is one merged area read: every entry decodes out of the
// single buffer covering [start, start+length) of one (area, dbNumber).
type s7ReadSegment struct {
	area     s7Area
	dbNumber int
	start    int
	length   int
	entries  []s7ReadTag
}

// planS7ReadSegments groups non-bit tags by (area, dbNumber) — never
// merging across areas or DBs — sorts each group by start offset and
// greedily merges neighbours into segments: two tags share a segment when
// the gap between them is at most s7MergeGapLimit bytes and the merged
// length stays within s7MaxPDUReadLen. Overlapping tags always merge.
// Groups are visited in ascending (area, dbNumber) order so the resulting
// read sequence is deterministic.
func planS7ReadSegments(tags []s7ReadTag) []s7ReadSegment {
	if len(tags) == 0 {
		return nil
	}

	type groupKey struct {
		area     s7Area
		dbNumber int
	}
	groups := make(map[groupKey][]s7ReadTag)
	var keys []groupKey
	for _, tag := range tags {
		k := groupKey{tag.addr.area, tag.addr.dbNumber}
		if _, seen := groups[k]; !seen {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], tag)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].area != keys[j].area {
			return keys[i].area < keys[j].area
		}
		return keys[i].dbNumber < keys[j].dbNumber
	})

	var segments []s7ReadSegment
	for _, k := range keys {
		group := groups[k]
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].addr.start < group[j].addr.start
		})
		for _, tag := range group {
			segments = appendMergedSegment(segments, k.area, k.dbNumber, tag)
		}
	}
	return segments
}

// appendMergedSegment extends the newest segment when tag fits within its
// gap and length limits, or starts a new segment. Because each group is
// sorted by start, only the newest segment of the current group can be a
// merge candidate. A tag whose own size exceeds s7MaxPDUReadLen still gets
// its own (oversized) segment; gos7 splits it into several telegrams.
func appendMergedSegment(segments []s7ReadSegment, area s7Area, dbNumber int, tag s7ReadTag) []s7ReadSegment {
	if n := len(segments); n > 0 {
		cur := &segments[n-1]
		if cur.area == area && cur.dbNumber == dbNumber {
			gap := tag.addr.start - (cur.start + cur.length)
			mergedLen := tag.addr.start + tag.addr.size - cur.start
			if gap <= s7MergeGapLimit && mergedLen <= s7MaxPDUReadLen {
				if mergedLen > cur.length {
					cur.length = mergedLen
				}
				cur.entries = append(cur.entries, tag)
				return segments
			}
		}
	}
	return append(segments, s7ReadSegment{
		area:     area,
		dbNumber: dbNumber,
		start:    tag.addr.start,
		length:   tag.addr.size,
		entries:  []s7ReadTag{tag},
	})
}

// readS7Area performs one area read of len(buf) bytes into buf through the
// client — the single choke point every Read-path PLC round trip goes
// through, whether merged or per-tag.
func readS7Area(client gos7.Client, area s7Area, dbNumber, start int, buf []byte) error {
	switch area {
	case areaDB:
		return client.AGReadDB(dbNumber, start, len(buf), buf)
	case areaM:
		return client.AGReadMB(start, len(buf), buf)
	case areaI:
		return client.AGReadEB(start, len(buf), buf)
	case areaQ:
		return client.AGReadAB(start, len(buf), buf)
	default:
		return fmt.Errorf("s7: unknown memory area %d", area)
	}
}

// readS7Batch reads one batch (at most s7MaxBatchSize tags) and returns one
// TagValue per input tag, in input order:
//
//   - Unknown tags fail immediately with no PLC traffic (unchanged).
//   - Non-bit tags are merged into planned segments; every tag in a failed
//     segment is marked QualityBad with that segment's error (PERF-003
//     behavior change: one failed round trip now fails its whole segment).
//   - Bit tags keep the tag-per-trip read of the previous implementation
//     (conservative first version: bits are never merged).
func (d *S7Driver) readS7Batch(client gos7.Client, batch []string, now time.Time) []core.TagValue {
	values := make([]core.TagValue, len(batch))
	var wordTags, bitTags []s7ReadTag

	// Phase 1 — resolve the batch under one config snapshot.
	d.RLock()
	for i, name := range batch {
		cfg, cfgOK := d.tags[name]
		addr, addrOK := d.addrs[name]
		if !cfgOK || !addrOK {
			values[i] = core.TagValue{
				Tag:       name,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     fmt.Errorf("tag not found: %s", name),
			}
			continue
		}
		tag := s7ReadTag{
			index:  i,
			name:   name,
			addr:   addr,
			scale:  cfg.Scale,
			offset: cfg.Offset,
		}
		tag.dt, _ = core.ParseDataType(cfg.Type)
		if addr.isBit {
			bitTags = append(bitTags, tag)
		} else {
			wordTags = append(wordTags, tag)
		}
	}
	d.RUnlock()

	// Phase 2 — merged reads for non-bit tags.
	for _, seg := range planS7ReadSegments(wordTags) {
		d.readMergedSegment(client, seg, now, values)
	}

	// Phase 3 — bit tags, one read each (unchanged semantics).
	for _, tag := range bitTags {
		values[tag.index] = d.readBitTag(client, tag, now)
	}

	return values
}

// readMergedSegment performs one merged area read and decodes every tag in
// the segment out of the shared buffer. When the read fails, every tag in
// the segment is marked QualityBad with the same error — one failed round
// trip fails its whole segment (registered PERF-003 behavior change).
func (d *S7Driver) readMergedSegment(client gos7.Client, seg s7ReadSegment, now time.Time, values []core.TagValue) {
	buf := make([]byte, seg.length)
	err := readS7Area(client, seg.area, seg.dbNumber, seg.start, buf)
	if err != nil {
		err = fmt.Errorf("s7 read error: %w", err)
		d.recordReadFailure(err)
		if util.IsConnectionError(err) {
			d.handleConnLostIfCurrent(client)
		}
		for _, tag := range seg.entries {
			values[tag.index] = core.TagValue{
				Tag:       tag.name,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     err,
			}
		}
		return
	}

	for _, tag := range seg.entries {
		off := tag.addr.start - seg.start
		// Zero-copy sub-slice of the segment buffer: decodeS7Buffer reads
		// the value immediately and never retains the buffer, so every
		// tag can share the one segment allocation instead of copying.
		sub := buf[off : off+tag.addr.size]
		val, derr := decodeS7Buffer(sub, tag.addr, tag.dt, &d.helper)
		if derr != nil {
			// Decode failures stay per-tag: they signal a type/address
			// mismatch of this one tag (e.g. DBB address with uint16
			// type), not a problem with the shared read.
			derr = fmt.Errorf("s7 read error: %w", derr)
			d.recordReadFailure(derr)
			values[tag.index] = core.TagValue{
				Tag:       tag.name,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     derr,
			}
			continue
		}
		values[tag.index] = s7GoodTagValue(tag, val, now)
	}
}

// readBitTag reads one bit tag with its own PLC round trip, mirroring the
// pre-merge per-tag error handling exactly.
func (d *S7Driver) readBitTag(client gos7.Client, tag s7ReadTag, now time.Time) core.TagValue {
	val, err := d.readAddress(client, tag.addr, tag.dt)
	if err != nil {
		d.recordReadFailure(err)
		if util.IsConnectionError(err) {
			d.handleConnLostIfCurrent(client)
		}
		return core.TagValue{
			Tag:       tag.name,
			Quality:   core.QualityBad,
			Timestamp: now,
			Error:     err,
		}
	}
	return s7GoodTagValue(tag, val, now)
}

// s7GoodTagValue applies scale/offset and wraps the decoded value in a
// QualityGood TagValue.
func s7GoodTagValue(tag s7ReadTag, val any, now time.Time) core.TagValue {
	val = util.ApplyTransform(val, tag.scale, tag.offset)
	return core.TagValue{
		Tag:       tag.name,
		Value:     val,
		Type:      tag.dt,
		Quality:   core.QualityGood,
		Timestamp: now,
	}
}

// recordReadFailure counts a failed PLC read and records its error string
// as the driver's last error.
func (d *S7Driver) recordReadFailure(err error) {
	d.RecordError()
	d.Lock()
	d.SetLastErrorLocked(err.Error())
	d.Unlock()
}
