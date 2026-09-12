package route

import (
	"encoding/json"
	"net/http"
)

func getRules(w http.ResponseWriter, r *http.Request) {
	stats := getEngine().GetRuleStats()
	render(w, r, http.StatusOK, map[string]any{"rules": stats})
}

type disableRuleRequest struct {
	Index    int  `json:"index"`
	Disabled bool `json:"disabled"`
}

func disableRule(w http.ResponseWriter, r *http.Request) {
	var req disableRuleRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	if err := getEngine().SetRuleDisabled(req.Index, req.Disabled); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	renderNoContent(w)
}
