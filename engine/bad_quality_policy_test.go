package engine

// docs/config/global.md documents global.engine.on-bad-quality as four policies
// for one QualityBad point, in a table precise enough to be wrong:
//
//   publish          正常发布（默认）        downstream handles the bad value
//   drop             丢弃，不进入规则匹配和发布
//   mark-and-publish 发布但清空 value（置 nil），保留 quality=bad
//   alert            正常发布并触发告警回调
//
// The subtle one is mark-and-publish: "clear the value, keep quality=bad" only
// means anything if the clearing happens BEFORE the fan-out and the point is
// still forwarded afterwards. Clearing after the broadcast would hand
// subscribers the device's last good number under a bad label; forwarding
// nothing would leave them no signal at all that the device had dropped. Both
// look fine in a diff, and neither shows up on a chart.
//
// Round 28 held write-min-interval (also from this page) to its sentence and
// found it false. This file does the same for the policy table.
//
// The engine is started for real here: dataBus and cache are created in Start(),
// and a test that calls processPoint on a shell is testing a nil map.

import (
	"context"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// badPoint is what a driver publishes when its read fails: a number that must
// NOT be believed, plus the label that says so. 999 is the device's last good
// reading carried forward, which is exactly the value a downstream chart would
// draw.
func badPoint(driver, tag string) core.DataPoint {
	return core.DataPoint{
		Driver:    driver,
		Tag:       tag,
		Value:     float64(999),
		Type:      core.TypeFloat64,
		Quality:   core.QualityBad,
		Timestamp: time.Now(),
	}
}

// waitPoint reads one point from ch, or reports that none arrived promptly.
func waitPoint(t *testing.T, ch <-chan core.DataPoint) (core.DataPoint, bool) {
	t.Helper()
	select {
	case p := <-ch:
		return p, true
	case <-time.After(300 * time.Millisecond):
		return core.DataPoint{}, false
	}
}

// startWithPolicy builds a real engine (New + Start) whose bad-quality policy is
// the one under test, and stops it when the test ends.
func startWithPolicy(t *testing.T, policy string) *CoreCEngine {
	t.Helper()
	e := New().(*CoreCEngine)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := &core.Config{
		Global: core.GlobalConfig{
			LogLevel: "error",
			Engine:   core.EngineConfig{OnBadQuality: policy},
		},
	}
	if err := e.Start(ctx, cfg); err != nil {
		t.Fatalf("engine start (%s): %v", policy, err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

// mark-and-publish: subscribers still receive the point, so they learn the device
// is bad, but Value is nil, so they cannot act on the number.
func TestBadQualityMarkAndPublishClearsValue(t *testing.T) {
	e := startWithPolicy(t, "mark-and-publish")

	ch, unsub := e.Subscribe("plc") // the filter matches Driver only, not "driver/tag"
	defer unsub()

	e.processing.processPoint(badPoint("plc", "t"))

	got, ok := waitPoint(t, ch)
	if !ok {
		t.Fatal("mark-and-publish dropped the point; subscribers never learn the device went bad")
	}
	if got.Value != nil {
		t.Errorf("Value = %v, want nil (documented: 清空 value)", got.Value)
	}
	if got.Quality != core.QualityBad {
		t.Errorf("Quality = %v, want bad (documented: 保留 quality=bad)", got.Quality)
	}
}

// drop: the point does not reach subscribers at all.
func TestBadQualityDropDoesNotReachSubscribers(t *testing.T) {
	e := startWithPolicy(t, "drop")

	ch, unsub := e.Subscribe("plc") // the filter matches Driver only, not "driver/tag"
	defer unsub()

	e.processing.processPoint(badPoint("plc", "t"))

	if p, ok := waitPoint(t, ch); ok {
		t.Fatalf("drop forwarded %+v to subscribers", p)
	}
}

// publish is the default and keeps the value as it arrived, so downstream can
// decide for itself rather than being handed a silent nil.
func TestBadQualityPublishKeepsValue(t *testing.T) {
	e := startWithPolicy(t, "publish")

	ch, unsub := e.Subscribe("plc") // the filter matches Driver only, not "driver/tag"
	defer unsub()

	e.processing.processPoint(badPoint("plc", "t"))

	got, ok := waitPoint(t, ch)
	if !ok {
		t.Fatal("publish did not forward the point")
	}
	if got.Value == nil {
		t.Error("publish cleared Value, but that is mark-and-publish's contract")
	}
}

// alert publishes AND alerts; a point that disappeared from the bus would mean
// the policy replaced forwarding instead of adding to it.
func TestBadQualityAlertStillForwards(t *testing.T) {
	e := startWithPolicy(t, "alert")

	ch, unsub := e.Subscribe("plc") // the filter matches Driver only, not "driver/tag"
	defer unsub()

	e.processing.processPoint(badPoint("plc", "t"))

	if _, ok := waitPoint(t, ch); !ok {
		t.Fatal("alert did not forward the point; docs say 正常发布并触发告警回调")
	}
	if got := e.ActiveAlertCount(); got == 0 {
		t.Error("alert forwarded the point but recorded no alert")
	}
}
