package statistic

import (
	"testing"
)

func TestStatisticManager(t *testing.T) {
	mgr := NewManager()

	mgr.PushRead(10)
	mgr.PushPublish(5)
	mgr.PushError()

	snap := mgr.Snapshot()
	if snap.ReadTotal != 10 || snap.PublishTotal != 5 || snap.ErrorTotal != 1 {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}
}
