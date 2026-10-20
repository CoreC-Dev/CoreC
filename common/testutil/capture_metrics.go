package testutil

import (
	"testing"

	"github.com/CoreC-Dev/CoreC/common/metrics"
)

// CaptureMetrics is a test helper that records driver-emitted metrics
// into an inspectable registry. It implements core.Metrics with the
// same accumulate/overwrite semantics as the production
// common/metrics.Registry, plus assertion helpers.
type CaptureMetrics struct {
	Reg *metrics.Registry
}

// NewCaptureMetrics creates an empty recorder.
func NewCaptureMetrics() *CaptureMetrics {
	return &CaptureMetrics{Reg: metrics.NewRegistry()}
}

// IncCounter records a counter increment (core.Metrics port).
func (c *CaptureMetrics) IncCounter(name string, labels map[string]string) {
	c.Reg.IncCounter(name, labels)
}

// SetGauge records a gauge overwrite (core.Metrics port).
func (c *CaptureMetrics) SetGauge(name string, value float64, labels map[string]string) {
	c.Reg.SetGauge(name, value, labels)
}

// AssertCounter fails the test unless the named counter series exists
// with exactly the wanted accumulated value.
func (c *CaptureMetrics) AssertCounter(t *testing.T, name string, labels map[string]string, want float64) {
	t.Helper()
	for _, m := range c.Reg.Snapshot() {
		if m.Name == name && labelsEqual(m.Labels, labels) {
			if m.Value != want {
				t.Errorf("%s%v = %v, want %v", name, labels, m.Value, want)
			}
			return
		}
	}
	t.Errorf("%s%v not recorded, want value %v", name, labels, want)
}

// AssertAbsent fails the test if the named series exists.
func (c *CaptureMetrics) AssertAbsent(t *testing.T, name string, labels map[string]string) {
	t.Helper()
	for _, m := range c.Reg.Snapshot() {
		if m.Name == name && labelsEqual(m.Labels, labels) {
			t.Errorf("%s%v unexpectedly recorded (value %v)", name, labels, m.Value)
		}
	}
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
