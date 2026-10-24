package route

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// getWriteHistory returns the write audit history: the most recent write
// attempts — successes AND failures — newest first, from the engine's
// bounded history. It complements GET /write/failed (the dead-letter
// queue, which only holds writes that exhausted all retries): control
// deployments need visibility into successful PLC writes too, e.g. for
// compliance tracing of who set which setpoint when.
//
// The engine optionally satisfies core.WriteAuditor. When it does not
// (alternative Engine implementations, test mocks), the endpoint reports
// 501 rather than 500 so clients can distinguish "write history not
// supported by this engine" from "no writes recorded yet" (which is a
// plain 200 with an empty list).
func getWriteHistory(w http.ResponseWriter, r *http.Request) {
	auditor, ok := getEngine().(core.WriteAuditor)
	if !ok {
		renderError(w, r, http.StatusNotImplemented, "write history not supported by this engine")
		return
	}
	// Server-side filtering for the compliance trail —
	// the full 1000-entry response is queryable by driver, tag, and
	// since-time (RFC 3339), with an optional result limit.
	entries := auditor.WriteHistory()
	// Filters allocate fresh slices: the WriteAuditor contract returns a
	// copy, but defensive copying here keeps the handler correct against
	// any implementation (an in-place filter via
	// entries[:0] would clobber a mock's stored history).
	q := r.URL.Query()
	if d := q.Get("driver"); d != "" {
		filtered := make([]core.WriteAuditEntry, 0, len(entries))
		for i := range entries {
			if entries[i].Driver == d {
				filtered = append(filtered, entries[i])
			}
		}
		entries = filtered
	}
	if t := q.Get("tag"); t != "" {
		filtered := make([]core.WriteAuditEntry, 0, len(entries))
		for i := range entries {
			if strings.Contains(entries[i].Tag, t) {
				filtered = append(filtered, entries[i])
			}
		}
		entries = filtered
	}
	if sv := q.Get("since"); sv != "" {
		since, err := time.Parse(time.RFC3339, sv)
		if err != nil {
			renderError(w, r, http.StatusBadRequest, "since must be RFC 3339 (e.g. 2026-10-20T12:00:00Z)")
			return
		}
		filtered := make([]core.WriteAuditEntry, 0, len(entries))
		for i := range entries {
			if !entries[i].Timestamp.Before(since) {
				filtered = append(filtered, entries[i])
			}
		}
		entries = filtered
	}
	if lv := q.Get("limit"); lv != "" {
		if n, err := strconv.Atoi(lv); err == nil && n >= 0 && n < len(entries) {
			entries = entries[:n]
		}
	}
	render(w, r, http.StatusOK, map[string]any{
		"writes": entries,
		"count":  len(entries),
	})
}
