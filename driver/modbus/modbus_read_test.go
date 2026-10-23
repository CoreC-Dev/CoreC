package modbus

import (
	"context"
	"testing"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// TestReadBatchNilClientErrors pins the demo-deploy crash fix (2026-10-22):
// a PLC power-cycle mid-read-cycle leaves the refreshed Modbus client nil
// until reconnect; the batch path previously dereferenced that nil and
// SIGSEGV'd, taking down the ENTIRE engine for an ordinary device fault.
// Now it returns a connection-class error (keyword-scanned by
// util.IsConnectionError, so retry paths treat it like any device fault).
func TestReadBatchNilClientErrors(t *testing.T) {
	b := &modbusBase{}
	batch := readBatch{area: areaHoldingRegister, start: 0, qty: 2}
	reqs := []batchTagReq{{
		name:      "t",
		cfg:       core.TagConfig{Name: "t", Address: "40001", Type: "uint16"},
		ai:        addrInfo{addr: 0, area: areaHoldingRegister},
		dt:        core.TypeUint16,
		qty:       1,
		batchable: true,
	}}

	_, err := b.readBatch(nil, batch, reqs)
	if err == nil {
		t.Fatal("nil client must return an error, not dereference nil")
	}
	if !util.IsConnectionError(err) {
		t.Errorf("nil-client error must be connection-class for retry handling, got: %v", err)
	}

	// readBatchWithRetry must also surface it (and keep the nil client
	// visible to the caller so performBatchReads can abort batching).
	ctx := context.Background()
	_, refreshed, err := b.readBatchWithRetry(ctx, nil, batch, reqs)
	if err == nil || refreshed != nil {
		t.Errorf("retry path with nil client: err=%v refreshed=%v, want connection error + nil client", err, refreshed)
	}
}

// TestReadTagNilClientErrors pins the per-tag sibling of the batch-path
// crash (E2E B8, 2026-10-23): when performBatchReads aborts on a lost
// connection and returns a nil refreshed client, Phase 3's fallback calls
// readTag(nil, ...) — whose first dereference SIGSEGV'd and crashed the
// engine twice in live testing. The batch path got a guard first; its
// abort exposed this one.
func TestReadTagNilClientErrors(t *testing.T) {
	b := &modbusBase{}
	ai := addrInfo{addr: 0, area: areaHoldingRegister}

	_, err := b.readTag(nil, ai, core.TypeFloat32, "t")
	if err == nil {
		t.Fatal("readTag(nil) must return an error, not dereference nil")
	}
	if !util.IsConnectionError(err) {
		t.Errorf("nil-client error must be connection-class, got: %v", err)
	}

	// And the retry wrapper must surface it without panicking on the
	// first attempt (the refresh path already checks nil; this is the
	// pre-refresh attempt).
	ctx := context.Background()
	req := batchTagReq{name: "t", ai: ai, dt: core.TypeFloat32, cfg: core.TagConfig{Name: "t", Address: "40001", Type: "float32"}}
	_, refreshed, _, err := b.readTagWithRetry(ctx, nil, req, "t")
	if err == nil {
		t.Fatal("readTagWithRetry(nil) must error")
	}
	if refreshed != nil {
		t.Errorf("refreshed client = %v, want nil", refreshed)
	}
}
