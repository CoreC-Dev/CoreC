package metrics

import (
	"sort"
	"sync"
)

// MetricKind classifies a registered metric for exposition.
type MetricKind string

const (
	KindCounter MetricKind = "counter"
	KindGauge   MetricKind = "gauge"
)

// Metric is one registered series snapshot for exposition.
type Metric struct {
	Name   string
	Kind   MetricKind
	Labels map[string]string
	Value  float64
}

// labelKey is the (name, sorted-labels) identity of a series. Sorting
// keeps identical label sets colliding onto one series regardless of
// map iteration order.
type labelKey struct {
	name   string
	labels string
}

func keyFor(name string, labels map[string]string) labelKey {
	if len(labels) == 0 {
		return labelKey{name: name}
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := make([]byte, 0, 64)
	for _, k := range keys {
		buf = append(buf, k...)
		buf = append(buf, '=')
		buf = append(buf, labels[k]...)
		buf = append(buf, ';')
	}
	return labelKey{name: name, labels: string(buf)}
}

// Registry is a tiny thread-safe store for driver-emitted metrics. It
// satisfies core.Metrics structurally (IncCounter/SetGauge signatures)
// so the engine can inject it into drivers via the existing SetMetrics
// port without either package importing the other — without it the
// port only ever reaches NoopMetrics and driver-emitted metrics never
// land in /metrics.
//
// Concurrency: one mutex for the whole registry is deliberate — the
// emission rate is per driver error, not per data point, so contention
// is negligible and the snapshot stays trivially consistent.
type Registry struct {
	mu       sync.Mutex
	counters map[labelKey]Metric
	gauges   map[labelKey]Metric
}

// NewRegistry creates an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters: make(map[labelKey]Metric),
		gauges:   make(map[labelKey]Metric),
	}
}

// IncCounter adds n (default 1) to the named counter series.
func (r *Registry) IncCounter(name string, labels map[string]string) {
	r.add(name, KindCounter, labels, 1, true)
}

// SetGauge overwrites the named gauge series value.
func (r *Registry) SetGauge(name string, value float64, labels map[string]string) {
	r.add(name, KindGauge, labels, value, false)
}

func (r *Registry) add(name string, kind MetricKind, labels map[string]string, delta float64, increment bool) {
	key := keyFor(name, labels)
	cp := make(map[string]string, len(labels))
	for k, v := range labels {
		cp[k] = v
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	registry := r.counters
	if kind == KindGauge {
		registry = r.gauges
	}
	m, ok := registry[key]
	if !ok {
		m = Metric{Name: name, Kind: kind, Labels: cp}
	}
	if increment {
		m.Value += delta
	} else {
		m.Value = delta
	}
	registry[key] = m
}

// Snapshot returns every registered series, sorted by name then labels
// for deterministic exposition. An empty registry returns nil.
func (r *Registry) Snapshot() []Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Metric, 0, len(r.counters)+len(r.gauges))
	for _, m := range r.counters {
		out = append(out, m)
	}
	for _, m := range r.gauges {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return keyFor(out[i].Name, out[i].Labels).labels < keyFor(out[j].Name, out[j].Labels).labels
	})
	return out
}
