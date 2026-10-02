package rule

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
)

// SetSubRules parses named sub-rule groups for SUB-RULE references.
func (e *Engine) SetSubRules(groups map[string][]core.RuleConfig) error {
	subEngines := make(map[string]*Engine, len(groups))
	for name, configs := range groups {
		sub := NewEngine()
		// sub-rules inherit providers from parent
		sub.providers = e.providers
		if err := sub.SetRules(configs); err != nil {
			return fmt.Errorf("sub-rule group %s: %w", name, err)
		}
		subEngines[name] = sub
	}

	// Circular reference detection
	if err := detectCircularSubRules(groups); err != nil {
		return err
	}

	e.mu.Lock()
	e.subEngines = subEngines
	e.mu.Unlock()
	return nil
}

// detectCircularSubRules checks for circular SUB-RULE references.
func detectCircularSubRules(groups map[string][]core.RuleConfig) error {
	for name := range groups {
		if err := checkCircular(name, groups, []string{}); err != nil {
			return err
		}
	}
	return nil
}

func checkCircular(name string, groups map[string][]core.RuleConfig, chain []string) error {
	for _, c := range chain {
		if c == name {
			return fmt.Errorf("circular sub-rule reference: %v -> %s", chain, name)
		}
	}
	chain = append(chain, name)
	for _, rc := range groups[name] {
		ref := extractSubRuleRef(rc.Match)
		if ref == "" {
			continue
		}
		if _, ok := groups[ref]; !ok {
			return fmt.Errorf("sub-rule group %q referenced by %q not found", ref, rc.Name)
		}
		if err := checkCircular(ref, groups, chain); err != nil {
			return err
		}
	}
	return nil
}

func extractSubRuleRef(match string) string {
	m := strings.TrimSpace(match)
	if strings.HasPrefix(strings.ToUpper(m), "SUB-RULE:") {
		return m[len("SUB-RULE:"):]
	}
	return ""
}

// SetRules parses rule configs and sets the active rule set.
func (e *Engine) SetRules(configs []core.RuleConfig) error {
	e.mu.RLock()
	providers := e.providers
	subEngines := e.subEngines
	e.mu.RUnlock()

	rules := make([]core.Rule, 0, len(configs))
	for _, cfg := range configs {
		r, err := newRule(cfg, providers, subEngines)
		if err != nil {
			return fmt.Errorf("failed to create rule %s: %w", cfg.Name, err)
		}
		rules = append(rules, r)
	}

	// Sort by priority (lower = higher priority)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority() < rules[j].Priority()
	})

	// Wrap each rule with statistics wrapper
	wrapped := make([]core.Rule, len(rules))
	for i, r := range rules {
		wrapped[i] = newRuleWrapper(r)
	}

	// Build driver index: rules with a detectable driver filter go into
	// per-driver buckets; all others (ALL rules, complex expressions,
	// rule-set/sub-rule refs) go into universalRules. Both lists inherit
	// the priority sort from the rules slice.
	driverIndex := make(map[string][]core.Rule)
	var universalRules []core.Rule
	for _, w := range wrapped {
		df := ""
		if f, ok := w.(core.RuleWrapper); ok {
			if inner, ok2 := f.Unwrap().(driverFilterer); ok2 {
				df = inner.DriverFilter()
			}
		}
		if df != "" {
			driverIndex[df] = append(driverIndex[df], w)
		} else {
			universalRules = append(universalRules, w)
		}
	}

	e.mu.Lock()
	e.rules = wrapped
	e.driverIndex = driverIndex
	e.universalRules = universalRules
	e.mu.Unlock()

	slog.Info("rules updated", "count", len(rules))
	return nil
}

// Rules returns the active rules.
func (e *Engine) Rules() []core.Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rules
}

// RuleStats returns runtime statistics for all rules.
func (e *Engine) RuleStats() []core.RuleStat {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]core.RuleStat, 0, len(e.rules))
	for i, r := range e.rules {
		w, ok := r.(core.RuleWrapper)
		if !ok {
			continue
		}
		inner := w.Unwrap()
		result = append(result, core.RuleStat{
			Index:     i,
			Name:      inner.Name(),
			Type:      inner.RuleType(),
			Match:     inner.Payload(),
			Action:    actionToString(inner.Action()),
			Target:    inner.Target(),
			Targets:   inner.Targets(),
			Priority:  inner.Priority(),
			Disabled:  w.IsDisabled(),
			HitCount:  w.HitCount(),
			HitAt:     w.HitAt(),
			MissCount: w.MissCount(),
			MissAt:    w.MissAt(),
		})
	}
	return result
}

// SetRuleDisabled enables or disables a rule by index.
func (e *Engine) SetRuleDisabled(index int, disabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if index < 0 || index >= len(e.rules) {
		return fmt.Errorf("rule index out of range: %d", index)
	}

	w, ok := e.rules[index].(core.RuleWrapper)
	if !ok {
		return fmt.Errorf("rule at index %d is not a RuleWrapper", index)
	}

	w.SetDisabled(disabled)
	slog.Info("rule disabled state changed", "index", index, "name", w.Name(), "disabled", disabled)
	return nil
}

func actionToString(a core.Action) string {
	switch a {
	case core.ActionForward:
		return "forward"
	case core.ActionDrop:
		return "drop"
	case core.ActionAlert:
		return "alert"
	case core.ActionTransform:
		return "transform"
	case core.ActionMirror:
		return "mirror"
	default:
		return "unknown"
	}
}

// ─── Rule Implementations ────────────────────────────────────────────

// newRule dispatches to the correct rule type based on the match expression.
func newRule(cfg core.RuleConfig, providers map[string]core.RuleProvider, subEngines map[string]*Engine) (core.Rule, error) {
	match := strings.TrimSpace(cfg.Match)
	upper := strings.ToUpper(match)

	// RULE-SET:name
	if strings.HasPrefix(upper, "RULE-SET:") {
		name := match[len("RULE-SET:"):]
		p, ok := providers[name]
		if !ok {
			return nil, fmt.Errorf("rule provider %q not found", name)
		}
		return newRuleSetRule(cfg, p)
	}

	// SUB-RULE:name
	if strings.HasPrefix(upper, "SUB-RULE:") {
		name := match[len("SUB-RULE:"):]
		sub, ok := subEngines[name]
		if !ok {
			return nil, fmt.Errorf("sub-rule group %q not found", name)
		}
		return newSubRuleRef(cfg, sub)
	}

	return newSimpleRule(cfg)
}

// simpleRule is a basic rule implementation using expression matching.
type simpleRule struct {
	name         string
	matchExpr    string
	action       core.Action
	target       string
	targets      []string
	priority     int
	expr         exprNode
	transform    *core.TransformConfig
	driverFilter string // extracted driver name if match is driver-scoped; "" otherwise
}

// driverFilterRe detects a top-level driver equality constraint at the
// start of a match expression, e.g. "driver == 'plc'" or
// "driver == 'plc' && tag =~ 'temp.*'". Rules with such a constraint
// can only match data points from that driver, so the engine can skip
// them for data points from other drivers.
var driverFilterRe = regexp.MustCompile(`(?i)^\s*driver\s*==\s*['"]([^'"]+)['"]\s*(?:&&|$)`)

func newSimpleRule(cfg core.RuleConfig) (*simpleRule, error) {
	action, err := parseAction(cfg.Action)
	if err != nil {
		return nil, err
	}

	targets := cfg.Targets
	if len(targets) == 0 && cfg.Target != "" {
		targets = []string{cfg.Target}
	}

	r := &simpleRule{
		name:         cfg.Name,
		matchExpr:    cfg.Match,
		action:       action,
		target:       cfg.Target,
		targets:      targets,
		priority:     cfg.Priority,
		transform:    cfg.Transform,
		driverFilter: extractDriverFilter(cfg.Match),
	}

	// Compile expression (ALL is handled separately at eval time)
	if !strings.EqualFold(cfg.Match, "ALL") {
		node, err := compileExpr(cfg.Match)
		if err != nil {
			return nil, fmt.Errorf("invalid match expression %q: %w", cfg.Match, err)
		}
		r.expr = node
	}

	return r, nil
}

// extractDriverFilter returns the driver name if the match expression
// has a top-level driver == 'name' constraint, or "" otherwise. This
// is a conservative heuristic: only expressions where driver equality
// appears at the start (optionally followed by &&) are recognized.
// Complex expressions like "tag =~ 'x' && driver == 'y'" return "",
// meaning the rule is treated as universal and evaluated for all drivers.
func extractDriverFilter(matchExpr string) string {
	if strings.EqualFold(matchExpr, "ALL") {
		return ""
	}
	m := driverFilterRe.FindStringSubmatch(matchExpr)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// DriverFilter returns the driver name this rule is scoped to, or "" if
// the rule can match data points from any driver. Used by the engine to
// build a driver index for faster matching.
func (r *simpleRule) DriverFilter() string { return r.driverFilter }

func (r *simpleRule) Match(point core.DataPoint) bool {
	expr := r.matchExpr

	// Special: match all
	if strings.EqualFold(expr, "ALL") {
		return true
	}

	if r.expr != nil {
		return r.expr.eval(point)
	}

	return false
}

func (r *simpleRule) Action() core.Action              { return r.action }
func (r *simpleRule) Target() string                   { return r.target }
func (r *simpleRule) Targets() []string                { return r.targets }
func (r *simpleRule) Priority() int                    { return r.priority }
func (r *simpleRule) Name() string                     { return r.name }
func (r *simpleRule) Payload() string                  { return r.matchExpr }
func (r *simpleRule) RuleType() string                 { return "simple" }
func (r *simpleRule) Transform() *core.TransformConfig { return r.transform }
func (r *simpleRule) String() string {
	return fmt.Sprintf("Rule{name=%s, match=%s, action=%d, target=%s, priority=%d}",
		r.name, r.matchExpr, r.action, r.target, r.priority)
}

// ruleSetRule delegates matching to a RuleProvider.
type ruleSetRule struct {
	name     string
	provider core.RuleProvider
	action   core.Action
	target   string
	targets  []string
	priority int
}

func newRuleSetRule(cfg core.RuleConfig, p core.RuleProvider) (*ruleSetRule, error) {
	action, err := parseAction(cfg.Action)
	if err != nil {
		return nil, err
	}
	targets := cfg.Targets
	if len(targets) == 0 && cfg.Target != "" {
		targets = []string{cfg.Target}
	}
	return &ruleSetRule{
		name:     cfg.Name,
		provider: p,
		action:   action,
		target:   cfg.Target,
		targets:  targets,
		priority: cfg.Priority,
	}, nil
}

func (r *ruleSetRule) Match(point core.DataPoint) bool {
	return r.provider.Match(point)
}
func (r *ruleSetRule) Action() core.Action              { return r.action }
func (r *ruleSetRule) Target() string                   { return r.target }
func (r *ruleSetRule) Targets() []string                { return r.targets }
func (r *ruleSetRule) Priority() int                    { return r.priority }
func (r *ruleSetRule) Name() string                     { return r.name }
func (r *ruleSetRule) Payload() string                  { return fmt.Sprintf("RULE-SET:%s", r.provider.Name()) }
func (r *ruleSetRule) RuleType() string                 { return "rule-set" }
func (r *ruleSetRule) Transform() *core.TransformConfig { return nil }
func (r *ruleSetRule) String() string {
	return fmt.Sprintf("RuleSet{name=%s, provider=%s, action=%d, target=%s}",
		r.name, r.provider.Name(), r.action, r.target)
}

// subRuleRef delegates matching to a named sub-rule group engine.
type subRuleRef struct {
	name      string
	subEngine *Engine
	action    core.Action
	target    string
	targets   []string
	priority  int
}

func newSubRuleRef(cfg core.RuleConfig, sub *Engine) (*subRuleRef, error) {
	action, err := parseAction(cfg.Action)
	if err != nil {
		return nil, err
	}
	targets := cfg.Targets
	if len(targets) == 0 && cfg.Target != "" {
		targets = []string{cfg.Target}
	}
	return &subRuleRef{
		name:      cfg.Name,
		subEngine: sub,
		action:    action,
		target:    cfg.Target,
		targets:   targets,
		priority:  cfg.Priority,
	}, nil
}

func (r *subRuleRef) Match(point core.DataPoint) bool {
	res := r.subEngine.Match(point)
	return res != nil
}
func (r *subRuleRef) Action() core.Action { return r.action }
func (r *subRuleRef) Target() string      { return r.target }
func (r *subRuleRef) Targets() []string   { return r.targets }
func (r *subRuleRef) Priority() int       { return r.priority }
func (r *subRuleRef) Name() string        { return r.name }
func (r *subRuleRef) Payload() string {
	return fmt.Sprintf("SUB-RULE:%s", r.name)
}
func (r *subRuleRef) RuleType() string                 { return "sub-rule" }
func (r *subRuleRef) Transform() *core.TransformConfig { return nil }
func (r *subRuleRef) String() string {
	return fmt.Sprintf("SubRule{name=%s, action=%d, target=%s}", r.name, r.action, r.target)
}

// ─── Helpers ────────────────────────────────────────────────────────

func parseAction(s string) (core.Action, error) {
	switch strings.ToLower(s) {
	case "forward":
		return core.ActionForward, nil
	case "drop":
		return core.ActionDrop, nil
	case "alert":
		return core.ActionAlert, nil
	case "transform":
		return core.ActionTransform, nil
	case "mirror":
		return core.ActionMirror, nil
	default:
		return 0, fmt.Errorf("unknown action: %s", s)
	}
}
