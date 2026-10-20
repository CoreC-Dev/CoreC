package route

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

	// Stamp the audit provenance at the trust boundary: this write came
	// from the control-plane API, and the caller's address is recorded
	// for the write audit history (GET /write/history). Both fields are
	// set after decoding, so a client cannot mislabel the audit trail by
	// sending source/remote in the request body.
	cmd.Source = "api"
	cmd.Remote = r.RemoteAddr

	var da core.DataAccessor = getEngine()
	res, err := da.WriteTag(r.Context(), cmd)
	if err != nil {
		// Distinguish client errors (wrong driver/tag name) from server
		// errors (internal processing failure). "not found" and
		// "not connected" are client-actionable (400); other errors are
		// 500 with a generic message to avoid leaking internals.
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "not connected") {
			slog.Error("tag write failed",
				"method", r.Method,
				"path", r.URL.Path,
				"driver", cmd.Driver,
				"tag", cmd.Tag,
				"remote", r.RemoteAddr,
				"error", err)
			renderError(w, r, http.StatusBadRequest, errMsg)
		} else {
			slog.Error("tag write failed",
				"method", r.Method,
				"path", r.URL.Path,
				"driver", cmd.Driver,
				"tag", cmd.Tag,
				"remote", r.RemoteAddr,
				"error", err)
			renderInternalError(w, r, err)
		}
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

// getFailedWrites returns dead-lettered write commands. By default it
// serves the engine's dead letter queue — commands that exhausted all
// retries against the device. With ?source=ingress it instead serves
// commands dropped at transport ingress (e.g. an MQTT command arriving
// while the command channel was full), which the transports keep in
// their own bounded stores (audit findings RES-1/TRA-4: those drops
// previously had only a counter, with no queryable detail of what was
// dropped and why).
func getFailedWrites(w http.ResponseWriter, r *http.Request) {
	entries, err := deadLetterEntriesForSource(r)
	if err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	render(w, r, http.StatusOK, map[string]any{
		"failed_writes": entries,
		"count":         len(entries),
	})
}

// deadLetterEntriesForSource resolves the ?source= query parameter to
// the right dead-letter store. The default (engine) path uses the
// StatsProvider role; source=ingress type-asserts the optional
// IngressDeadLetterProvider role. Unknown source values are rejected
// rather than silently falling back to the engine queue.
func deadLetterEntriesForSource(r *http.Request) ([]core.DeadLetterEntry, error) {
	switch source := r.URL.Query().Get("source"); source {
	case "", "engine":
		var sp core.StatsProvider = getEngine()
		return sp.DeadLetterEntries(), nil
	case "ingress":
		if provider, ok := getEngine().(core.IngressDeadLetterProvider); ok && provider != nil {
			return provider.IngressDeadLetters(), nil
		}
		// Engine without ingress dead-letter support: an empty list,
		// indistinguishable from "no drops" (same shape as the mock
		// engines in tests).
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown source %q (valid: engine, ingress)", source)
	}
}
