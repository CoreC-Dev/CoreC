package route

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// annotateStaleness returns a new map with the IsStale flag set on
// DataPoints whose Timestamp is older than now - threshold. If the
// threshold is 0 (disabled), the original map is returned unchanged.
// A new map is always allocated when annotation is active to avoid
// mutating the shared cache snapshot (which would race with concurrent
// API calls).
func annotateStaleness(tags map[string]core.DataPoint, threshold time.Duration) map[string]core.DataPoint {
	if threshold <= 0 || len(tags) == 0 {
		return tags
	}
	cutoff := time.Now().Add(-threshold)
	result := make(map[string]core.DataPoint, len(tags))
	for k := range tags {
		dp := tags[k]
		if dp.Timestamp.Before(cutoff) {
			dp.IsStale = true
		}
		result[k] = dp
	}
	return result
}

// getAllTags depends on the DataAccessor and StatsProvider roles of the
// engine (latest values + staleness threshold). It snapshots the engine
// once so both reads observe the same engine instance.
func getAllTags(w http.ResponseWriter, r *http.Request) {
	e := getEngine()
	var da core.DataAccessor = e
	var sp core.StatsProvider = e
	tags := annotateStaleness(da.LatestValues(""), sp.StaleThreshold())
	render(w, r, http.StatusOK, map[string]any{"tags": tags})
}

// writeTag only depends on the DataAccessor role of the engine.
func writeTag(w http.ResponseWriter, r *http.Request) {
	var cmd core.WriteCommand
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&cmd); err != nil {
		slog.Info("tag write rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	var da core.DataAccessor = getEngine()
	res, err := da.WriteTag(r.Context(), cmd)
	if err != nil {
		slog.Error("tag write failed",
			"method", r.Method,
			"path", r.URL.Path,
			"driver", cmd.Driver,
			"tag", cmd.Tag,
			"remote", r.RemoteAddr,
			"error", err)
		renderInternalError(w, r, err)
		return
	}

	// Audit the accepted write. The value is intentionally NOT logged —
	// it may carry process-sensitive data. Driver/tag names are safe and
	// let an operator trace which point was written.
	slog.Info("tag written",
		"method", r.Method,
		"path", r.URL.Path,
		"driver", cmd.Driver,
		"tag", cmd.Tag,
		"success", res.Success,
		"remote", r.RemoteAddr)

	render(w, r, http.StatusOK, res)
}

// getFailedWrites returns the dead letter queue — write commands that
// failed after all retries. It only depends on the StatsProvider role.
func getFailedWrites(w http.ResponseWriter, r *http.Request) {
	var sp core.StatsProvider = getEngine()
	entries := sp.DeadLetterEntries()
	render(w, r, http.StatusOK, map[string]any{
		"failed_writes": entries,
		"count":         len(entries),
	})
}
