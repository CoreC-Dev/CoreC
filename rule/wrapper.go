package rule

import (
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// ruleWrapper decorates any core.Rule with runtime statistics and
// enable/disable control, without modifying the underlying rule's logic.
type ruleWrapper struct {
	core.Rule
	disabled  atomic.Bool
	hitCount  atomic.Uint64
	hitAt     atomicTime
	missCount atomic.Uint64
	missAt    atomicTime
}

func (r *ruleWrapper) IsDisabled() bool {
	return r.disabled.Load()
}

func (r *ruleWrapper) SetDisabled(v bool) {
	r.disabled.Store(v)
}

func (r *ruleWrapper) HitCount() uint64 {
	return r.hitCount.Load()
}

func (r *ruleWrapper) HitAt() time.Time {
	return r.hitAt.Load()
}

func (r *ruleWrapper) MissCount() uint64 {
	return r.missCount.Load()
}

func (r *ruleWrapper) MissAt() time.Time {
	return r.missAt.Load()
}

func (r *ruleWrapper) Unwrap() core.Rule {
	return r.Rule
}

func (r *ruleWrapper) Match(point core.DataPoint) bool {
	if r.IsDisabled() {
		return false
	}
	ok := r.Rule.Match(point)
	if ok {
		r.hitCount.Add(1)
		r.hitAt.Store(time.Now())
	} else {
		r.missCount.Add(1)
		r.missAt.Store(time.Now())
	}
	return ok
}

func newRuleWrapper(r core.Rule) *ruleWrapper {
	return &ruleWrapper{Rule: r}
}

// atomicTime stores time as an atomic.Int64 of UnixNano.
// This avoids the GC pressure of boxing time.Time on every write
// (unlike a mutex-based approach).
type atomicTime struct {
	i atomic.Int64
}

func (t *atomicTime) Store(v time.Time) {
	t.i.Store(v.UnixNano())
}

func (t *atomicTime) Load() time.Time {
	return time.Unix(0, t.i.Load())
}
