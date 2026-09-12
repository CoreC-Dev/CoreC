package route

import (
	"net/http"
)

func getStats(w http.ResponseWriter, r *http.Request) {
	stats := getEngine().Stats()
	render(w, r, http.StatusOK, stats)
}
