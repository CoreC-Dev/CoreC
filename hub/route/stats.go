package route

import (
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

// getStats only depends on the StatsProvider role of the engine.
func getStats(w http.ResponseWriter, r *http.Request) {
	var sp core.StatsProvider = getEngine()
	render(w, r, http.StatusOK, sp.Stats())
}
