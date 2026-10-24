package core

import "time"

// ConfigAuditEntry records one successful configuration mutation (from
// PUT /configs and PATCH /configs) so "who changed what, when" is
// answerable from the API, not only from server logs. Bounded,
// newest-first, in-memory (restart clears
// it; the on-disk truth is the config file + the /configs/backups
// snapshot set).
type ConfigAuditEntry struct {
	At time.Time `json:"at"`
	// Action is the mutation kind: "reload" (PUT /configs, backup
	// restore) or "patch" (PATCH /configs).
	Action string `json:"action"`
	// Remote is the API caller's address, when known.
	Remote string `json:"remote,omitempty"`
	// Keys summarizes the change: patch keys, "payload" for
	// editor-driven PUTs, or the loaded path.
	Keys []string `json:"keys,omitempty"`
	// Details carries free-form context (e.g. the restored backup name).
	Details string `json:"details,omitempty"`
}
