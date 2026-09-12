package route

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func getTransports(w http.ResponseWriter, r *http.Request) {
	transports := getEngine().ListTransports()
	render(w, r, http.StatusOK, map[string]any{"transports": transports})
}

func getTransport(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	transports := getEngine().ListTransports()
	for _, t := range transports {
		if t.Name == name {
			render(w, r, http.StatusOK, t)
			return
		}
	}
	renderError(w, r, http.StatusNotFound, "transport not found")
}
