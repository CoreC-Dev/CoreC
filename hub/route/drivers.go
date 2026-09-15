package route

import (
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/go-chi/chi/v5"
)

// getDrivers only depends on the DriverManager role of the engine.
func getDrivers(w http.ResponseWriter, r *http.Request) {
	var dm core.DriverManager = getEngine()
	render(w, r, http.StatusOK, map[string]any{"drivers": dm.ListDrivers()})
}

// getDriver only depends on the DriverManager role of the engine.
func getDriver(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var dm core.DriverManager = getEngine()
	for _, d := range dm.ListDrivers() {
		if d.Name == name {
			render(w, r, http.StatusOK, d)
			return
		}
	}
	renderError(w, r, http.StatusNotFound, "driver not found")
}

// getDriverTags depends on the DataAccessor and StatsProvider roles of
// the engine (latest values + staleness threshold). It snapshots the
// engine once so both reads observe the same engine instance.
func getDriverTags(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	e := getEngine()
	var da core.DataAccessor = e
	var sp core.StatsProvider = e
	tags := annotateStaleness(da.LatestValues(name), sp.StaleThreshold())
	render(w, r, http.StatusOK, map[string]any{"tags": tags})
}
