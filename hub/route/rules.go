package route

import (
	"encoding/json"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

// getRules only depends on the RuleManager role of the engine.
func getRules(w http.ResponseWriter, r *http.Request) {
	var rm core.RuleManager = getEngine()
	render(w, r, http.StatusOK, map[string]any{"rules": rm.GetRuleStats()})
}

type disableRuleRequest struct {
	Index    int  `json:"index"`
	Disabled bool `json:"disabled"`
}

// disableRule only depends on the RuleManager role of the engine.
func disableRule(w http.ResponseWriter, r *http.Request) {
	var req disableRuleRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	var rm core.RuleManager = getEngine()
	if err := rm.SetRuleDisabled(req.Index, req.Disabled); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	renderNoContent(w)
}
