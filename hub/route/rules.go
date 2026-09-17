package route

import (
	"encoding/json"
	"log/slog"
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
		slog.Info("rule disable rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	var rm core.RuleManager = getEngine()
	if err := rm.SetRuleDisabled(req.Index, req.Disabled); err != nil {
		slog.Error("rule disable failed",
			"method", r.Method,
			"path", r.URL.Path,
			"rule_index", req.Index,
			"disabled", req.Disabled,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Resolve the rule name for a friendlier audit trail. The index is
	// valid here (SetRuleDisabled succeeded), so the lookup is safe.
	ruleName := ruleNameAt(rm, req.Index)
	msg := "rule disabled"
	if !req.Disabled {
		msg = "rule enabled"
	}
	slog.Info(msg,
		"method", r.Method,
		"path", r.URL.Path,
		"rule", ruleName,
		"rule_index", req.Index,
		"remote", r.RemoteAddr)

	renderNoContent(w)
}

// ruleNameAt returns the name of the rule at the given index, or "" if the
// index is out of range or the stats cannot be read. It is a best-effort
// lookup used only to enrich audit log records.
func ruleNameAt(rm core.RuleManager, index int) string {
	if index < 0 {
		return ""
	}
	stats := rm.GetRuleStats()
	if index >= len(stats) {
		return ""
	}
	return stats[index].Name
}
