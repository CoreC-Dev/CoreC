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
	// Name addresses the rule by its configured name (preferred). Rule
	// names are unique (enforced by config validation) and stable across
	// config reloads, unlike an index which shifts when rules are
	// reordered or removed.
	Name string `json:"name"`
	// Index addresses the rule by position; kept for backward
	// compatibility with clients that predate name addressing.
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

	var err error
	if req.Name != "" {
		// Name addressing wins when both fields are present: it is the
		// stable identifier and the index may be stale.
		err = rm.SetRuleDisabledByName(req.Name, req.Disabled)
	} else {
		// Legacy index addressing: index defaults to 0 when omitted.
		err = rm.SetRuleDisabled(req.Index, req.Disabled)
	}
	if err != nil {
		slog.Error("rule disable failed",
			"method", r.Method,
			"path", r.URL.Path,
			"rule", req.Name,
			"rule_index", req.Index,
			"disabled", req.Disabled,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Resolve the rule name for a friendlier audit trail. When the request
	// used name addressing it is already known; otherwise the index is
	// valid here (SetRuleDisabled succeeded), so the lookup is safe.
	ruleName := req.Name
	if ruleName == "" {
		ruleName = ruleNameAt(rm, req.Index)
	}
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
