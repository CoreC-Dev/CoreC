// Package rule provides the rule matching and routing engine for CoreC data points.
package rule

import (
	"sync"

	"github.com/CoreC-Dev/CoreC/core"
)

// Engine is the rule matching engine.
// It evaluates DataPoints against a priority-sorted list of rules.
// Rules with a detectable driver filter are indexed by driver name so
// that a data point from driver X only evaluates rules scoped to X
// (plus universal rules that can match any driver). Rules scoped to
// other drivers have their miss statistics updated without evaluating
// their match expressions. This preserves the priority-order and
// statistics invariants documented in the Match method while reducing
// the per-data-point cost from O(all rules) to O(relevant rules).
type Engine struct {
	mu             sync.RWMutex
	rules          []core.Rule
	driverIndex    map[string][]core.Rule // driver → rules scoped to that driver (priority-sorted)
	universalRules []core.Rule            // rules without a driver filter (priority-sorted)
	providers      map[string]core.RuleProvider
	subEngines     map[string]*Engine // sub-rule groups
}

// driverFilterer is an optional interface that rules implement to
// declare they only match data points from a specific driver.
type driverFilterer interface {
	DriverFilter() string // driver name, or "" if no filter
}

// missRecorder is an optional interface that rule wrappers implement to
// record a miss without evaluating the match expression.
type missRecorder interface {
	RecordMiss()
}

// Compile-time assertion that the concrete rule.Engine satisfies the
// core.RuleEngine port. This keeps the engine package decoupled from the
// rule adapter: it depends only on core.RuleEngine.
var _ core.RuleEngine = (*Engine)(nil)

// NewEngine creates a new rule engine.
func NewEngine() *Engine {
	return &Engine{
		providers:  make(map[string]core.RuleProvider),
		subEngines: make(map[string]*Engine),
	}
}

// AddProvider registers a rule provider for RULE-SET references.
func (e *Engine) AddProvider(p core.RuleProvider) {
	e.mu.Lock()
	e.providers[p.Name()] = p
	e.mu.Unlock()
}

// closer is an optional interface that rule providers may implement to
// stop background goroutines (e.g. FileProvider's reloadLoop).
type closer interface {
	Close()
}

// CloseProviders closes and removes all registered rule providers,
// stopping any background goroutines they started (e.g. reload loops).
// This should be called during engine Stop/Reload to prevent goroutine
// leaks (problem 4).
func (e *Engine) CloseProviders() {
	e.mu.Lock()
	for name, p := range e.providers {
		if c, ok := p.(closer); ok {
			c.Close()
		}
		delete(e.providers, name)
	}
	e.mu.Unlock()
}

// ProviderCount returns the number of registered rule providers.
func (e *Engine) ProviderCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.providers)
}

// MatchResult contains the matched rule and resolved targets. It is an
// alias for core.RuleMatchResult so the concrete rule.Engine satisfies the
// core.RuleEngine port (whose Match returns *core.RuleMatchResult) without
// any conversion at the call site.
type MatchResult = core.RuleMatchResult

// Match finds the first matching rule for a DataPoint.
// Rules are evaluated in priority order (lower priority = higher precedence).
// Returns nil if no rule matches.
//
// Match uses a driver index to reduce the per-data-point scan cost.
// Rules with a detectable driver == 'X' filter are only evaluated for
// data points from driver X; for data points from other drivers, their
// miss statistics are updated directly without evaluating the match
// expression. Universal rules (ALL, complex expressions, rule-set/sub-rule
// refs) are always evaluated. Both groups are priority-sorted, and the
// two sorted lists are merged on the fly to preserve global priority
// order — the first match across both groups is returned, exactly as a
// full linear scan would produce. Miss statistics for rules scoped to
// other drivers are recorded via RecordMiss, preserving the invariant
// that every non-matching, non-disabled rule records a miss.
func (e *Engine) Match(point core.DataPoint) *MatchResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Fast path: no driver index (e.g. sub-engines without SetRules
	// rebuilding, or all rules are universal) → fall back to linear scan.
	driverRules := e.driverIndex[point.Driver]
	if len(e.driverIndex) == 0 {
		return e.matchInRules(point, e.rules)
	}

	// Merge universalRules and driverRules (both priority-sorted) on the
	// fly, evaluating in global priority order. Record misses for rules
	// scoped to other drivers.
	result := e.matchMerged(point, e.universalRules, driverRules)

	// Record misses for rules scoped to other drivers. In the original
	// linear scan, rules after the first match are NOT evaluated, so we
	// only record misses for rules with strictly lower priority than the
	// match. If no match, all rules would have been evaluated.
	var matchPriority int
	matched := result != nil
	if matched {
		matchPriority = result.Rule.Priority()
	}
	for driver, rules := range e.driverIndex {
		if driver == point.Driver {
			continue
		}
		for _, r := range rules {
			if matched && r.Priority() >= matchPriority {
				continue // would not have been reached in the linear scan
			}
			if mr, ok := r.(missRecorder); ok {
				mr.RecordMiss()
			}
		}
	}

	return result
}
