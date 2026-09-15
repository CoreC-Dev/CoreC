package core

// RuleMatchResult contains the matched rule and resolved targets for a
// DataPoint evaluated by a RuleEngine. It is the port-level equivalent of
// rule.MatchResult; the rule package aliases its MatchResult to this type
// so the concrete adapter satisfies the RuleEngine port without conversion.
type RuleMatchResult struct {
	Rule      Rule
	Targets   []string
	Transform *TransformConfig
}

// RuleEngine is the port (hexagonal architecture) for the rule matching
// and routing engine. The engine package depends on this interface rather
// than the concrete *rule.Engine adapter, so the rule implementation can be
// swapped or mocked without touching the engine.
//
// The method set is exactly the set of operations the engine calls on its
// rule engine (plus Rules, used by Stats, and ProviderCount, used by
// reload tests). The concrete rule.Engine satisfies this interface; see
// the compile-time assertion in rule/engine.go.
type RuleEngine interface {
	// SetRules parses rule configs and sets the active rule set.
	SetRules(configs []RuleConfig) error

	// SetSubRules parses named sub-rule groups for SUB-RULE references.
	SetSubRules(groups map[string][]RuleConfig) error

	// Match finds the first matching rule for a DataPoint in priority
	// order. Returns nil if no rule matches.
	Match(point DataPoint) *RuleMatchResult

	// RuleStats returns runtime statistics for all rules.
	RuleStats() []RuleStat

	// SetRuleDisabled enables or disables a rule by index.
	SetRuleDisabled(index int, disabled bool) error

	// AddProvider registers a rule provider for RULE-SET references.
	AddProvider(p RuleProvider)

	// CloseProviders closes and removes all registered rule providers,
	// stopping any background goroutines they started.
	CloseProviders()

	// ProviderCount returns the number of registered rule providers.
	ProviderCount() int

	// Rules returns the active rules. Used by the engine to report the
	// configured rule count in Stats().
	Rules() []Rule
}
