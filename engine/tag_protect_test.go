package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// registerTaggedDriver installs a recording driver plus the driverConfigs
// snapshot the write-protection guard consults. Tests drive WriteTag /
// executeWriteWithRetry directly, so no full Start() is needed.
func registerTaggedDriver(e *CoreCEngine, name string, tags []core.TagConfig) *recordingDriver {
	d := newRecordingDriver(name)
	e.driverReg.drivers[name] = d
	e.driverReg.driverConfigs[name] = core.DriverConfig{Name: name, Type: "mock", Tags: tags}
	return d
}

// TestWriteProtectReadOnlyRejectsSingleWrite: a read-only tag must be
// rejected before the driver sees the command, and the rejection must be
// auditable and carry the reason.
func TestWriteProtectReadOnlyRejectsSingleWrite(t *testing.T) {
	e := newTestEngine(t)
	d := registerTaggedDriver(e, "plc", []core.TagConfig{
		{Name: "setpoint", Address: "40010", Type: "float32", Protect: "read-only"},
		{Name: "writable", Address: "40011", Type: "float32"},
	})

	_, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "setpoint", Value: 1.0, Type: core.TypeFloat32})
	if err == nil {
		t.Fatal("expected write to a read-only tag to be rejected")
	}
	if !strings.Contains(err.Error(), "read-only") || !strings.Contains(err.Error(), "setpoint") {
		t.Errorf("error should name the tag and the protection, got %q", err.Error())
	}
	if got := len(d.GetWritten()); got != 0 {
		t.Errorf("driver must not receive the write, got %d command(s)", got)
	}

	// The rejection is recorded in the write audit history.
	hist := e.WriteHistory()
	if len(hist) != 1 || hist[0].Success {
		t.Fatalf("expected one failed audit entry, got %+v", hist)
	}
	if !strings.Contains(hist[0].Error, "read-only") {
		t.Errorf("audit entry should carry the reason, got %q", hist[0].Error)
	}

	// A tag without protection still writes normally (no behaviour change).
	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "writable", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("unprotected tag must remain writable: %v", err)
	}
	if got := len(d.GetWritten()); got != 1 {
		t.Errorf("expected the unprotected write to reach the driver, got %d", got)
	}
}

// TestWriteProtectReadOnlyStopsCommandPath: the MQTT command path funnels
// through WriteTag too and must dead-letter immediately (no retries) —
// retrying a configuration-level rejection can never succeed.
func TestWriteProtectReadOnlyStopsCommandPath(t *testing.T) {
	e := newTestEngine(t)
	d := registerTaggedDriver(e, "plc", []core.TagConfig{
		{Name: "setpoint", Address: "40010", Type: "float32", Protect: "read-only"},
	})

	e.writes.executeWriteWithRetry(core.WriteCommand{Driver: "plc", Tag: "setpoint", Value: 1.0, Type: core.TypeFloat32})

	if got := len(d.GetWritten()); got != 0 {
		t.Errorf("driver must not receive the command, got %d", got)
	}
	dl := e.writes.DeadLetterEntries()
	if len(dl) != 1 {
		t.Fatalf("expected the rejected command in the dead-letter queue, got %d entries", len(dl))
	}
	if !strings.Contains(dl[0].Error, "read-only") {
		t.Errorf("dead letter should carry the reason, got %q", dl[0].Error)
	}
	if dl[0].Attempts != 1 {
		t.Errorf("config-level rejection must not retry: attempts = %d, want 1", dl[0].Attempts)
	}
}

// TestWriteProtectWriteOnlyNotScheduled: write-only tags accept writes but
// must never be collected.
func TestWriteProtectWriteOnlyNotScheduled(t *testing.T) {
	e := newTestEngine(t)
	registerTaggedDriver(e, "plc", nil)
	// AddTask launches the task goroutine immediately, so the callbacks must be
	// non-nil no-ops and the scheduler must be stopped on cleanup (goleak).
	e.lifecycle.scheduler = NewScheduler(
		func(context.Context, string, []string) ([]core.TagValue, error) { return nil, nil },
		func(string, []core.TagValue, int) {},
		0,
	)
	t.Cleanup(func() { _ = e.lifecycle.scheduler.Stop() })

	e.driverReg.scheduleDriverTags(core.DriverConfig{
		Name: "plc",
		Tags: []core.TagConfig{
			{Name: "readable", Address: "40001", Type: "float32", Interval: "1s"},
			{Name: "cmd_only", Address: "40002", Type: "float32", Interval: "1s", Protect: "write-only"},
		},
	})

	s := e.lifecycle.scheduler
	s.mu.RLock()
	schedTasks := make([]*taskRunner, 0, len(s.tasks))
	for _, r := range s.tasks {
		schedTasks = append(schedTasks, r)
	}
	s.mu.RUnlock()

	if len(schedTasks) != 1 {
		t.Fatalf("expected one schedule task (readable only), got %d", len(schedTasks))
	}
	for _, runner := range schedTasks {
		for _, tag := range runner.task.Tags {
			if tag == "cmd_only" {
				t.Errorf("write-only tag must not be scheduled, task tags = %v", runner.task.Tags)
			}
		}
		if len(runner.task.Tags) != 1 || runner.task.Tags[0] != "readable" {
			t.Errorf("expected the readable tag only, got %v", runner.task.Tags)
		}
	}

	// The write path still accepts the write-only tag.
	d := e.driverReg.drivers["plc"].(*recordingDriver)
	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "plc", Tag: "cmd_only", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("write-only tag must accept writes: %v", err)
	}
	if got := len(d.GetWritten()); got != 1 {
		t.Errorf("expected the write to reach the driver, got %d", got)
	}
}

// TestTagWriteProtectionReason unit-tests the reason helper, including the
// two cases that must stay permissive: absent tag (left to the driver's
// "tag not found") and explicit read-write.
func TestTagWriteProtectionReason(t *testing.T) {
	tags := []core.TagConfig{
		{Name: "ro", Protect: "read-only"},
		{Name: "rw", Protect: "read-write"},
		{Name: "plain"},
		{Name: "wo", Protect: "write-only"},
	}
	for _, tc := range []struct {
		tag  string
		want string
	}{
		{"ro", "read-only"},
		{"rw", ""},
		{"plain", ""},
		{"wo", ""},     // write-only is about reads, not writes
		{"absent", ""}, // unknown tag keeps the driver's own error
	} {
		got := tagWriteProtectionReason(tags, tc.tag)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: expected no rejection, got %q", tc.tag, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: expected reason containing %q, got %q", tc.tag, tc.want, got)
		}
	}

	if !isWriteOnlyTag(tags, "wo") || isWriteOnlyTag(tags, "ro") || isWriteOnlyTag(tags, "absent") {
		t.Error("isWriteOnlyTag misclassified the tag set")
	}
}

// TestWriteProtectAbsentConfigSnapshot: when the engine has no driverConfigs
// entry (e.g. a driver registered without a config snapshot), the guard must
// stay out of the way and preserve the historical behaviour.
func TestWriteProtectAbsentConfigSnapshot(t *testing.T) {
	e := newTestEngine(t)
	d := newRecordingDriver("bare")
	e.driverReg.drivers["bare"] = d // deliberately no driverConfigs entry

	if _, err := e.WriteTag(context.Background(), core.WriteCommand{Driver: "bare", Tag: "any", Value: 1.0, Type: core.TypeFloat32}); err != nil {
		t.Fatalf("write without a config snapshot must not be rejected: %v", err)
	}
	if got := len(d.GetWritten()); got != 1 {
		t.Errorf("expected the write to reach the driver, got %d", got)
	}
}
