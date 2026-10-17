package engine

import (
	"strings"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// PreviewRule (POST /rules/preview) evaluates a match expression against
// a sample point in a scratch rule engine, without touching the active
// rule set, and surfaces compile errors verbatim.

// newPreviewEngine returns the concrete engine behind engine.New().
func newPreviewEngine(t *testing.T) *CoreCEngine {
	t.Helper()
	e, ok := New().(*CoreCEngine)
	if !ok {
		t.Fatal("engine.New() did not return *CoreCEngine")
	}
	return e
}

func TestPreviewRuleMatch(t *testing.T) {
	e := newPreviewEngine(t)
	point := core.DataPoint{Driver: "plc1", Tag: "temperature", Value: 96.5, Type: core.TypeFloat64}

	matched, err := e.PreviewRule("tag == 'temperature' && value > 95", point)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if !matched {
		t.Error("matched=false, want true (value 96.5 > 95)")
	}
}

func TestPreviewRuleNoMatch(t *testing.T) {
	e := newPreviewEngine(t)
	point := core.DataPoint{Driver: "plc1", Tag: "temperature", Value: 20.0}

	matched, err := e.PreviewRule("tag == 'temperature' && value > 95", point)
	if err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}
	if matched {
		t.Error("matched=true, want false (value 20 < 95)")
	}
}

func TestPreviewRuleInvalidExpression(t *testing.T) {
	e := newPreviewEngine(t)
	point := core.DataPoint{Tag: "temperature", Value: 20.0}

	_, err := e.PreviewRule("tag ==", point)
	if err == nil {
		t.Fatal(`expected a compile error for the invalid expression "tag ==", got nil`)
	}
	// The error must be verbatim enough for the frontend to display it:
	// it identifies the rule ("preview") and the underlying expr error.
	if !strings.Contains(err.Error(), "preview") {
		t.Errorf("error should identify the scratch rule by name, got %q", err.Error())
	}
}

func TestPreviewRuleDoesNotTouchActiveRules(t *testing.T) {
	e := newPreviewEngine(t)
	if err := e.SetRules([]core.RuleConfig{{Name: "prod", Match: "ALL", Action: "forward"}}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}

	if _, err := e.PreviewRule("tag == 'preview-only'", core.DataPoint{Tag: "x"}); err != nil {
		t.Fatalf("PreviewRule: %v", err)
	}

	stats := e.GetRuleStats()
	if len(stats) != 1 || stats[0].Name != "prod" {
		t.Fatalf("active rule set changed by preview: %+v", stats)
	}
}
