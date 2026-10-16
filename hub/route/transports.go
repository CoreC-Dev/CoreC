package route

import (
	"net/http"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/go-chi/chi/v5"
)

// getTransports only depends on the TransportManager role of the engine.
func getTransports(w http.ResponseWriter, r *http.Request) {
	var tm core.TransportManager = getEngine()
	render(w, r, http.StatusOK, map[string]any{"transports": tm.ListTransports()})
}

// getTransport only depends on the TransportManager role of the engine.
func getTransport(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var tm core.TransportManager = getEngine()
	for _, t := range tm.ListTransports() {
		if t.Name == name {
			render(w, r, http.StatusOK, t)
			return
		}
	}
	renderError(w, r, http.StatusNotFound, "transport not found")
}

// testTransport publishes a test data point to the named transport, allowing
// users to verify connectivity (e.g. Slack webhook, email SMTP) from the
// Dashboard without waiting for a real alert.
func testTransport(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var tm core.TransportManager = getEngine()
	transport, ok := tm.GetTransport(name)
	if !ok {
		renderError(w, r, http.StatusNotFound, "transport not found")
		return
	}
	testPoint := core.DataPoint{
		Driver:    "test",
		Device:    "test",
		Tag:       "test-notification",
		Value:     1,
		Type:      core.TypeInt16,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
		Metadata:  map[string]string{"rule": "test"},
	}
	if err := transport.Publish(r.Context(), testPoint); err != nil {
		render(w, r, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	render(w, r, http.StatusOK, map[string]any{"ok": true, "message": "test notification sent"})
}
