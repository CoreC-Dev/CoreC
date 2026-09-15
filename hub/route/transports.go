package route

import (
	"net/http"

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
