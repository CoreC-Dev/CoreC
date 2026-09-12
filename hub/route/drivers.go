package route

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func getDrivers(w http.ResponseWriter, r *http.Request) {
	drivers := getEngine().ListDrivers()
	render(w, r, http.StatusOK, map[string]any{"drivers": drivers})
}

func getDriver(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	drivers := getEngine().ListDrivers()
	for _, d := range drivers {
		if d.Name == name {
			render(w, r, http.StatusOK, d)
			return
		}
	}
	renderError(w, r, http.StatusNotFound, "driver not found")
}

func getDriverTags(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	tags := getEngine().LatestValues(name)
	render(w, r, http.StatusOK, map[string]any{"tags": tags})
}
