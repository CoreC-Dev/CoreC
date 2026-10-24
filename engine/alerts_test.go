package engine

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// testAlertRule is a minimal core.Rule for driving the registry.
type testAlertRule struct{ name string }

func (testAlertRule) Match(core.DataPoint) bool        { return true }
func (r testAlertRule) Name() string                   { return r.name }
func (testAlertRule) Action() core.Action              { return core.ActionAlert }
func (testAlertRule) Target() string                   { return "" }
func (testAlertRule) Targets() []string                { return nil }
func (testAlertRule) Priority() int                    { return 0 }
func (testAlertRule) Payload() string                  { return "value > 90" }
func (testAlertRule) RuleType() string                 { return "simple" }
func (testAlertRule) Transform() *core.TransformConfig { return nil }
func (testAlertRule) Continue() bool                   { return false }
func (r testAlertRule) String() string                 { return r.name }

func alertPoint(driver, tag string, value any) core.DataPoint {
	return core.DataPoint{Driver: driver, Tag: tag, Value: value, Quality: core.QualityGood, Timestamp: time.Now()}
}

// TestAlertRegistryFireMergeStream pins the fire phase of the alert
// registry: creating alerts, merging repeated fires, and streaming
// exactly one event per identity.
func TestAlertRegistryFireMergeStream(t *testing.T) {
	am := newAlertManager(time.Hour)
	ch, unsub := am.subscribe()
	defer unsub()
	rule := testAlertRule{"high-temp"}

	am.record(rule, alertPoint("plc1", "temp", 91))
	am.record(rule, alertPoint("plc1", "temp", 92))
	am.record(rule, alertPoint("plc1", "hum", 65)) // same rule, other tag: second alert

	active := am.activeAlerts()
	if len(active) != 2 {
		t.Fatalf("active alerts = %d, want 2", len(active))
	}
	var temp *core.Alert
	for i := range active {
		if active[i].Tag == "temp" {
			temp = &active[i]
		}
	}
	if temp == nil {
		t.Fatal("temp alert missing")
	}
	if temp.Count != 2 {
		t.Errorf("temp fire count = %d, want 2 (fires merge into one alert)", temp.Count)
	}

	// Exactly one fired event per alert identity reached the stream.
	fired := drainEvents(t, ch, 2, time.Second)
	for _, ev := range fired {
		if ev.Type != core.AlertFired {
			t.Errorf("stream event type = %q, want fired", ev.Type)
		}
	}
}

// TestAlertRegistryAckSticks pins the ack phase: ack persists across
// re-fires and unknown keys error.
func TestAlertRegistryAckSticks(t *testing.T) {
	am := newAlertManager(time.Hour)
	rule := testAlertRule{"high-temp"}

	am.record(rule, alertPoint("plc1", "temp", 91))
	if err := am.ack(alertKey(rule, "plc1", "temp")); err != nil {
		t.Fatalf("ack: %v", err)
	}
	am.record(rule, alertPoint("plc1", "temp", 95)) // re-fire after ack

	var temp *core.Alert
	active := am.activeAlerts()
	for i := range active {
		if active[i].Tag == "temp" {
			temp = &active[i]
		}
	}
	if temp == nil {
		t.Fatal("temp alert missing after re-fire")
	}
	if !temp.Acked {
		t.Error("ack did not stick across the re-fire")
	}
	if temp.Count != 2 {
		t.Errorf("post-ack fire count = %d, want 2", temp.Count)
	}
	if err := am.ack("nope"); err == nil {
		t.Error("expected error acking unknown key")
	}
}

// TestAlertRegistrySilenceClears pins the clear phase: after the active
// window passes with no re-fire, the sweep moves alerts to history with
// cleared events, final state (incl. acked) intact.
func TestAlertRegistrySilenceClears(t *testing.T) {
	am := newAlertManager(200 * time.Millisecond) // short window for the test
	rule := testAlertRule{"high-temp"}

	am.record(rule, alertPoint("plc1", "temp", 91))
	if err := am.ack(alertKey(rule, "plc1", "temp")); err != nil {
		t.Fatalf("ack: %v", err)
	}

	time.Sleep(350 * time.Millisecond)
	am.sweep(time.Now())
	if got := len(am.activeAlerts()); got != 0 {
		t.Fatalf("active after window = %d, want 0 (cleared)", got)
	}
	hist := am.alertHistory()
	if len(hist) == 0 {
		t.Fatal("history empty after clear")
	}
	sawCleared, sawAcked := false, false
	for _, ev := range hist {
		if ev.Type == core.AlertCleared {
			sawCleared = true
			if ev.Alert.Acked {
				sawAcked = true
			}
		}
	}
	if !sawCleared {
		t.Error("no cleared event in history")
	}
	if !sawAcked {
		t.Error("cleared temp alert lost its acked flag in history")
	}
}

// TestAlertHistoryCap verifies the history ring is bounded.
func TestAlertHistoryCap(t *testing.T) {
	am := newAlertManager(time.Hour)
	rule := testAlertRule{"r"}
	for i := 0; i < alertHistoryCap+37; i++ {
		am.record(rule, alertPoint("d", "t", i))
		// Each distinct tag creates an alert + one fired event; acking
		// adds a second event per alert to reach the cap faster.
		am.ack(alertKey(rule, "d", "t"))
	}
	if len(am.history) > alertHistoryCap {
		t.Errorf("history length = %d, exceeds cap %d", len(am.history), alertHistoryCap)
	}
	// Newest-first ordering: the first history entry is the newest ack.
	hist := am.alertHistory()
	if len(hist) == 0 || hist[0].Type != core.AlertAcked {
		t.Errorf("newest history entry = %+v, want the latest ack", hist[0])
	}
}

// TestAlertRegistryCap verifies the active registry evicts the
// oldest-fired alerts when a pathological rule floods the space.
func TestAlertRegistryCap(t *testing.T) {
	am := newAlertManager(time.Hour)
	rule := testAlertRule{"flood"}
	for i := 0; i < alertRegistryCap+10; i++ {
		am.record(rule, alertPoint("d", "t", i))
	}
	if got := len(am.active); got > alertRegistryCap {
		t.Errorf("registry size = %d, exceeds cap %d", got, alertRegistryCap)
	}
}

// TestFireAlertRecordsInRegistry drives the integration end: a rule
// with action=alert reaching processPoint must populate the registry,
// not just reach the OnAlert log handler.
func TestFireAlertRecordsInRegistry(t *testing.T) {
	e := New().(*CoreCEngine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &core.Config{
		Rules: []core.RuleConfig{
			{Name: "hot", Match: "value > 90", Action: "alert"},
		},
	}
	if err := e.Start(ctx, cfg); err != nil {
		t.Fatalf("engine start: %v", err)
	}
	defer e.Stop()

	e.processing.processPoint(core.DataPoint{
		Driver: "plc1", Tag: "temp", Value: 95.0,
		Type: core.TypeFloat64, Quality: core.QualityGood, Timestamp: time.Now(),
	})

	active := e.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("active alerts = %d, want 1", len(active))
	}
	if active[0].Rule != "hot" || active[0].Tag != "temp" {
		t.Errorf("alert = %+v, want rule hot on tag temp", active[0])
	}
	if got := e.ActiveAlertCount(); got != 1 {
		t.Errorf("ActiveAlertCount = %d, want 1", got)
	}
	if got := e.AlertFireCounts()["hot"]; got != 1 {
		t.Errorf("fire count = %d, want 1", got)
	}
	if hist := e.AlertHistory(); len(hist) != 1 || hist[0].Type != core.AlertFired {
		t.Errorf("history = %+v, want one fired event", hist)
	}
}

// drainEvents collects up to n stream events within the window.
func drainEvents(t *testing.T, ch <-chan core.AlertEvent, n int, window time.Duration) []core.AlertEvent {
	t.Helper()
	var out []core.AlertEvent
	deadline := time.After(window)
	for len(out) < n {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-deadline:
			if len(out) < n {
				t.Fatalf("timed out waiting for %d events, got %d", n, len(out))
			}
			return out
		}
	}
	return out
}
