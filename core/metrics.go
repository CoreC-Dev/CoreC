package core

// Metrics is the port for emitting observability metrics (counters and
// gauges) from drivers and other components. Defining it as an interface
// in the core port package keeps concrete metric backends (Prometheus,
// statsd, no-op, …) out of the domain and lets each component depend on
// exactly this capability.
//
// NOTE: This port is defined but NOT yet injected into drivers. Wiring it
// through Driver.Init / a driver constructor is a larger, separate
// refactor; the goal here is to establish the port so future work can
// adopt it without changing the core contract.
type Metrics interface {
	// IncCounter increments a named counter by 1, tagged with labels.
	IncCounter(name string, labels map[string]string)
	// SetGauge sets a named gauge to value, tagged with labels.
	SetGauge(name string, value float64, labels map[string]string)
}

// NoopMetrics is a Metrics implementation that discards every call. It is
// the zero-dependency default for drivers and components that do not emit
// metrics, so they can hold a Metrics value without a real backend.
type NoopMetrics struct{}

// IncCounter satisfies Metrics; it does nothing.
func (NoopMetrics) IncCounter(name string, labels map[string]string) {}

// SetGauge satisfies Metrics; it does nothing.
func (NoopMetrics) SetGauge(name string, value float64, labels map[string]string) {}

// Compile-time assertion that NoopMetrics satisfies Metrics.
var _ Metrics = NoopMetrics{}
