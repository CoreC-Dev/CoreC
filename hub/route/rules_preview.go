package route

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
)

// previewRuleRequest is the request body of POST /rules/preview. Point is
// a full DataPoint (already JSON-tagged in core), so a rule author can
// paste a live sample from /tags and tweak it.
type previewRuleRequest struct {
	// Match is the rule match expression under test, e.g.
	// "tag == 'temperature' && value > 95".
	Match string `json:"match"`
	// Point is the sample data point to evaluate the expression against.
	Point core.DataPoint `json:"point"`
}

// previewRule trial-runs a match expression against a sample data point
// (offline rule preview). The expression is compiled and evaluated in a
// scratch rule engine — the active rule set is never touched — so rule
// authors can validate syntax and semantics BEFORE saving, instead of
// discovering a compile error only when production data flows through a
// saved rule.
func previewRule(w http.ResponseWriter, r *http.Request) {
	var req previewRuleRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		slog.Info("rule preview rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Match == "" {
		slog.Info("rule preview rejected",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", "empty match expression")
		renderError(w, r, http.StatusBadRequest, "match is required")
		return
	}

	previewer, ok := getEngine().(core.RulePreviewer)
	if !ok {
		renderError(w, r, http.StatusNotImplemented, "rule preview not supported by this engine")
		return
	}

	matched, err := previewer.PreviewRule(req.Match, req.Point)
	if err != nil {
		// Compile/eval errors are returned verbatim with 400 (not masked
		// as 500): they are the answer the caller asked for, and the
		// frontend shows the message next to the expression editor.
		slog.Info("rule preview failed",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
			"error", err)
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("rule preview evaluated",
		"method", r.Method,
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
		"matched", matched)

	render(w, r, http.StatusOK, map[string]any{"matched": matched})
}
