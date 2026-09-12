package route

import (
	"encoding/json"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

func getAllTags(w http.ResponseWriter, r *http.Request) {
	tags := getEngine().LatestValues("")
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
