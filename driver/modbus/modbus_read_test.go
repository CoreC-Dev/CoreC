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
