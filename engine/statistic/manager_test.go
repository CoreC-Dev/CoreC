package statistic

import (
	"testing"
)

func TestStatisticManager(t *testing.T) {
	mgr := NewManager()

	mgr.PushRead(10)
	mgr.PushPublish(5)
	mgr.PushError()

	read, pub := mgr.Total()
	if read != 10 || pub != 5 {
		t.Fatalf("expected total read=10, pub=5, got read=%d, pub=%d", read, pub)
	}

	if mgr.Errors() != 1 {
		t.Fatalf("expected 1 error, got %d", mgr.Errors())
	}

	// Manually simulate a ticker blip swap
	mgr.readBlip.Store(mgr.readTemp.Swap(0))
	mgr.publishBlip.Store(mgr.publishTemp.Swap(0))

	rPerSec, pPerSec := mgr.Now()
	if rPerSec != 10 || pPerSec != 5 {
		t.Fatalf("expected rates 10, 5; got %d, %d", rPerSec, pPerSec)
	}

	snap := mgr.Snapshot()
	if snap.ReadTotal != 10 || snap.PublishTotal != 5 || snap.ErrorTotal != 1 {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}

	mgr.ResetStatistic()
	readAfter, pubAfter := mgr.Total()
	if readAfter != 0 || pubAfter != 0 || mgr.Errors() != 0 {
		t.Fatalf("expected 0 after reset, got read=%d, pub=%d, err=%d", readAfter, pubAfter, mgr.Errors())
	}

	if mgr.Uptime() <= 0 {
		t.Fatal("uptime should be positive")
	}
}

func TestDropCounter(t *testing.T) {
	mgr := NewManager()

	if mgr.Drops() != 0 {
		t.Fatalf("expected 0 drops initially, got %d", mgr.Drops())
	}

	mgr.PushDrop(5)
	mgr.PushDrop(3)

	if mgr.Drops() != 8 {
		t.Fatalf("expected 8 drops, got %d", mgr.Drops())
	}

	snap := mgr.Snapshot()
	if snap.DropTotal != 8 {
		t.Fatalf("expected snapshot DropTotal=8, got %d", snap.DropTotal)
	}

	mgr.ResetStatistic()
	if mgr.Drops() != 0 {
		t.Fatalf("expected 0 drops after reset, got %d", mgr.Drops())
	}
}
