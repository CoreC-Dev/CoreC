package route

import (
	"net/http"

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
	entries := auditor.WriteHistory()
	render(w, r, http.StatusOK, map[string]any{
		"writes": entries,
		"count":  len(entries),
	})
}
