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
// disable, and preview API surface. CoreCEngine keeps thin forwarders
// to this role so the
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

// PreviewRule compiles the given match expression and evaluates it
// against the sample point, satisfying core.RulePreviewer
// (POST /rules/preview). The active rule set is never touched.
//
// Both failure classes propagate to the API:
// compile errors and evaluation errors alike. Swallowing an
// evaluation failure into a bare false would make a type-mismatched
// expression preview as "not matched"
// instead of telling the author why.
func (r *ruleRole) PreviewRule(match string, point core.DataPoint) (bool, error) {
	return rule.PreviewMatch(match, point)
}
