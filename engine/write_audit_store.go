package engine

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/CoreC-Dev/CoreC/core"
)

// writeAuditTrail is the bounded in-memory history of write attempts
// (GET /write/history), optionally mirrored to disk
// (global.engine.write-audit-path publishing to ERROR).
//
// Keeping this state in its own sub-struct (like deadLetterState) is what
// keeps writeManager's method count under the role limit pinned by
// structure_test.go.
type writeAuditTrail struct {
	mu    sync.Mutex
	items []core.WriteAuditEntry // oldest first

	// store is the optional on-disk persistence. nil = memory-only.
	store *jsonlStore[core.WriteAuditEntry]
}

// record appends one attempt, evicting the oldest when full and mirroring the
// record to disk when persistence is configured. The in-memory ring stays the
// primary structure; a disk failure never loses or blocks the audit entry.
func (t *writeAuditTrail) record(entry core.WriteAuditEntry) {
	t.mu.Lock()
	t.items = append(t.items, entry)
	if len(t.items) > writeAuditCapacity {
		t.items = t.items[len(t.items)-writeAuditCapacity:]
	}
	store := t.store
	t.mu.Unlock()
	// Append outside the ring's lock scope while still serialising file
	// writes against each other: the store owns its own mutex.
	store.append(entry)
}

// newestFirst returns a copy of the trail, most recent entry first. The result
// is always non-nil so it serializes as a JSON array.
func newestFirstLocked(items []core.WriteAuditEntry) []core.WriteAuditEntry {
	result := make([]core.WriteAuditEntry, len(items))
	for i := range items {
		result[len(items)-1-i] = items[i]
	}
	return result
}

// configure opens the optional on-disk trail and replays the newest persisted
// entries into the in-memory ring, so GET /write/history is warm after a
// restart. A failed open is start-blocking: the operator asked for a durable
// trail and must not silently get an in-memory one.
func (t *writeAuditTrail) configure(path string) error {
	t.mu.Lock()
	t.store = nil
	t.items = nil
	t.mu.Unlock()
	if path == "" {
		return nil
	}
	store, err := newJSONLStore[core.WriteAuditEntry](path, 0)
	if err != nil {
		return fmt.Errorf("write audit store: %w", err)
	}
	replayed := store.load(writeAuditCapacity)
	t.mu.Lock()
	t.items = replayed
	t.store = store
	t.mu.Unlock()
	if len(replayed) > 0 {
		slog.Info("write audit store: replayed persisted entries",
			"path", path, "count", len(replayed))
	}
	return nil
}

// close flushes and closes the store, so a late record cannot append to a
// closed file.
func (t *writeAuditTrail) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.store.close()
}
