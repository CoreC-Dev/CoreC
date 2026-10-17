// Package engine implements the core.Engine interface and orchestration pipeline.
package engine

import (
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/rule"
)

// ruleRole groups the engine's rule management: the active rule engine,
// its configuration entrypoint (initRules), and the rule inspection,
// disable, and preview API surface. Extracted from CoreCEngine in the
// CPLX-020 God Object split; CoreCEngine keeps thin forwarders so the
// core.Engine / core.RuleManager / core.RulePreviewer method sets are
// unchanged.
type ruleRole struct {
	ruleEngine core.RuleEngine
}

// initRules sets up rule providers, sub-rule groups, and top-level rules.
func (r *ruleRole) initRules(config *core.Config) error {
	for _, pc := range config.RuleProviders {
		p, err := rule.NewFileProvider(pc.Name, pc.Path, pc.Interval)
		if err != nil {
			slog.Error("failed to create rule provider", "name", pc.Name, "error", err)
			return fmt.Errorf("failed to create rule provider %s: %w", pc.Name, err)
		}
		r.ruleEngine.AddProvider(p)
	}

	if len(config.RuleGroups) > 0 {
		if err := r.ruleEngine.SetSubRules(config.RuleGroups); err != nil {
			return fmt.Errorf("failed to set sub-rules: %w", err)
		}
	}

	if err := r.SetRules(config.Rules); err != nil {
		return fmt.Errorf("failed to set rules: %w", err)
	}
	return nil
}

func (r *ruleRole) SetRules(rules []core.RuleConfig) error {
	return r.ruleEngine.SetRules(rules)
}

func (r *ruleRole) GetRuleStats() []core.RuleStat {
	return r.ruleEngine.RuleStats()
}

func (r *ruleRole) SetRuleDisabled(index int, disabled bool) error {
	return r.ruleEngine.SetRuleDisabled(index, disabled)
}

func (r *ruleRole) SetRuleDisabledByName(name string, disabled bool) error {
	return r.ruleEngine.SetRuleDisabledByName(name, disabled)
}

// PreviewRule compiles the given match expression in a scratch rule
// engine and evaluates it against the sample point, satisfying
// core.RulePreviewer (POST /rules/preview). The active rule set is never
// touched: the scratch engine is built per call and discarded, so a
// broken expression cannot poison production routing. Compile errors are
// returned verbatim so the API can surface them to the rule author.
//
// The scratch rule uses a synthetic name ("preview") and a forward
// action; action/target validity is a config.validate concern and
// irrelevant here — only the match expression is under test.
func (r *ruleRole) PreviewRule(match string, point core.DataPoint) (bool, error) {
	scratch := rule.NewEngine()
	if err := scratch.SetRules([]core.RuleConfig{{
		Name:   "preview",
		Match:  match,
		Action: "forward",
	}}); err != nil {
		return false, err
	}
	return len(scratch.Match(point)) > 0, nil
}
