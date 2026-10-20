package executor

import (
	"sync"

	"github.com/CoreC-Dev/CoreC/core"
)

// configAuditCap bounds the in-memory config change audit trail exposed
// via GET /configs/audit. Same order of magnitude as the write audit
// history (1000): enough for hours of debugging, cheap in memory.
const configAuditCap = 500

// ConfigAuditEntry lives in core (see core/audit.go): both the executor
// (recorder) and the route layer (exposer) need the wire type.

// configAudit is the bounded, newest-first audit trail. Guarded by its
// own small mutex so recording never contends the executor's config
// lock (mirrors the write-audit design in engine/write_manager.go).
type configAudit struct {
	mu      sync.Mutex
	entries []core.ConfigAuditEntry
}

var cfgAudit = &configAudit{}

// recordConfigAudit appends one entry, evicting the oldest past the cap.
func recordConfigAudit(entry core.ConfigAuditEntry) {
	cfgAudit.mu.Lock()
	defer cfgAudit.mu.Unlock()
	cfgAudit.entries = append(cfgAudit.entries, entry)
	if len(cfgAudit.entries) > configAuditCap {
		cfgAudit.entries = cfgAudit.entries[len(cfgAudit.entries)-configAuditCap:]
	}
}

// ConfigAuditHistory returns the most recent entries, newest first.
// Exported for the route layer via the executor's function-injection
// wiring (same pattern as ReloadFunc/GetConfigFunc — avoids an import
// cycle with hub/route).
func ConfigAuditHistory() []core.ConfigAuditEntry {
	cfgAudit.mu.Lock()
	defer cfgAudit.mu.Unlock()
	out := make([]core.ConfigAuditEntry, len(cfgAudit.entries))
	for i := range cfgAudit.entries {
		out[len(cfgAudit.entries)-1-i] = cfgAudit.entries[i]
	}
	return out
}
