package metrics

import (
	"sync/atomic"
	"time"
)

// rateWindowSeconds is the sliding window over which RateWindow reports
// throughput. One minute matches the shortest window a Prometheus
// operator would scrape at and keeps per-second resolution meaningful
// for "is collection live right now" alerting.
const rateWindowSeconds = 60

// rateCountBits is the width of the per-second count packed into each
// bucket word (low bits). 24 bits saturates a single second's count at
// 16.7M points — far above any realistic per-second ingest — so the
// saturation cannot distort a real rate.
const rateCountBits = 24

const rateCountMax = uint64(1)<<rateCountBits - 1

// RateWindow is a lock-free fixed-window rate counter: 60 one-second
// buckets, each stamped with the unix second it belongs to. It reports
// the total number of observations over the last complete window so
// EngineStats.PointsPerSec can be a true "current rate" instead of a
// lifetime average, which after a stall keeps decaying for hours
// instead of dropping to zero.
//
// Each bucket is a single atomic uint64 packing (unixSecond << 24 |
// count), so Add and Sum are wait-free and safe for concurrent use:
//   - Add stamps the bucket for the current second; a bucket whose
//     stamp is older (a gap in traffic longer than one second) resets
//     its count instead of accumulating into a stale second.
//   - Sum only counts buckets stamped within the last 60 seconds, so
//     reads that stopped a minute ago report a rate of zero.
//
// Sum(now) returns the window total; divide by min(window, uptime) to
// get points per second (the caller owns the uptime so a fresh engine
// reports honestly instead of diluting its first seconds).
type RateWindow struct {
	buckets [rateWindowSeconds]atomic.Uint64
}

// NewRateWindow creates an empty rate window.
func NewRateWindow() *RateWindow {
	return &RateWindow{}
}

// pack folds a unix second and a per-second count into one bucket word.
func pack(sec, count uint64) uint64 {
	if count > rateCountMax {
		count = rateCountMax
	}
	return sec<<rateCountBits | count
}

// unpack splits a bucket word back into its second and count.
func unpack(w uint64) (sec, count uint64) {
	return w >> rateCountBits, w & rateCountMax
}

// Observe records n points against the given instant's second. The
// instant is injected so tests can drive the window without sleeping.
func (r *RateWindow) Observe(now time.Time, n uint64) {
	if n == 0 {
		return
	}
	sec := uint64(now.Unix())
	slot := sec % rateWindowSeconds
	b := &r.buckets[slot]
	for {
		old := b.Load()
		oldSec, oldCount := unpack(old)
		var newWord uint64
		if oldSec != sec {
			// The bucket belongs to a different (older) second:
			// restart it for the current second.
			newWord = pack(sec, n)
		} else {
			newWord = pack(sec, oldCount+n)
		}
		if b.CompareAndSwap(old, newWord) {
			return
		}
	}
}

// Sum returns the number of observations recorded in the last complete
// window ending at now (buckets stamped (now-60, now]). Stale buckets —
// seconds with no traffic, or traffic older than the window — are
// excluded, so a pipeline that stopped reporting a minute ago sums to
// zero. The instant is injected for the same reason as Observe.
func (r *RateWindow) Sum(now time.Time) uint64 {
	sec := uint64(now.Unix())
	var total uint64
	for i := range r.buckets {
		w := r.buckets[i].Load()
		bSec, bCount := unpack(w)
		if bCount == 0 {
			continue
		}
		// Count only buckets inside the window (now-60, now]. The
		// distance form (instead of comparing to now-60) keeps the
		// arithmetic unsigned: bSec <= sec holds by construction of
		// the stamp, so sec-bSec can never underflow — a literal
		// `bSec > sec-60` comparison would wrap around for a window
		// reaching back before second 60.
		if bSec <= sec && sec-bSec < rateWindowSeconds {
			total += bCount
		}
	}
	return total
}

// WindowSeconds reports the window length in seconds so callers can
// divide consistently (min(window, uptime)).
func (r *RateWindow) WindowSeconds() int64 {
	return rateWindowSeconds
}
