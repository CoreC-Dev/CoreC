package config

import (
	"fmt"

	"github.com/CoreC-Dev/CoreC/core"
)

// validateRuleGroups makes the SUB-RULE contract explicit: a group member's
// match is aggregated, but its target/targets/transform/continue are ignored
// by the referencing rule. Rejecting those fields turns a silent no-op into a
// configuration error instead of "configured but never executed".
func validateRuleGroups(cfg *core.Config) error {
	for group, rules := range cfg.RuleGroups {
		if group == "" {
			return fmt.Errorf("rule group name cannot be empty")
		}
		names := make(map[string]bool, len(rules))
		for _, r := range rules {
			if r.Name == "" {
				return fmt.Errorf("rule group %s: member rule name cannot be empty", group)
			}
			if names[r.Name] {
				return fmt.Errorf("rule group %s: duplicate member rule name %q", group, r.Name)
			}
			names[r.Name] = true
			if r.Match == "" {
				return fmt.Errorf("rule group %s: member rule %s: match cannot be empty", group, r.Name)
			}
			if r.Target != "" || len(r.Targets) > 0 || r.Transform != nil || r.Continue {
				return fmt.Errorf("rule group %s: member rule %s sets target/targets/transform/continue, which a SUB-RULE reference ignores — move those to the referencing rule", group, r.Name)
			}
		}
	}
	return nil
}
