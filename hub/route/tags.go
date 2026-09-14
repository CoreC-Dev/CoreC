package route

import (
	"encoding/json"
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

func getAllTags(w http.ResponseWriter, r *http.Request) {
	tags := getEngine().LatestValues("")
	tags = annotateStaleness(tags, getEngine().StaleThreshold())
	render(w, r, http.StatusOK, map[string]any{"tags": tags})
}

func writeTag(w http.ResponseWriter, r *http.Request) {
	var cmd core.WriteCommand
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&cmd); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	res, err := getEngine().WriteTag(r.Context(), cmd)
	if err != nil {
		renderInternalError(w, r, err)
		return
	}

	render(w, r, http.StatusOK, res)
}

// getFailedWrites returns the dead letter queue — write commands that
// failed after all retries.
func getFailedWrites(w http.ResponseWriter, r *http.Request) {
	entries := getEngine().DeadLetterEntries()
	render(w, r, http.StatusOK, map[string]any{
		"failed_writes": entries,
		"count":         len(entries),
	})
}
