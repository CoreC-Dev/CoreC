package rule

import (
	"github.com/CoreC-Dev/CoreC/core"
)

// matchMerged evaluates two priority-sorted rule lists in global priority
// order and collects all matching rules until a terminal match (a rule
// with Continue() == false). It uses a two-pointer merge so the evaluation
// order is identical to a single sorted list.
//
// When every rule is terminal (Continue() == false, the default), the
// first match is returned and the slice contains exactly one element —
// the former first-match-wins behavior.
func (e *Engine) matchMerged(point core.DataPoint, listA, listB []core.Rule) []*MatchResult {
	var results []*MatchResult
	i, j := 0, 0
	for i < len(listA) && j < len(listB) {
		var r core.Rule
		if listA[i].Priority() <= listB[j].Priority() {
			r = listA[i]
			i++
		} else {
			r = listB[j]
			j++
		}
		if result := e.tryMatch(point, r); result != nil {
			results = append(results, result)
			if !r.Continue() {
				return results // terminal: stop collecting
			}
		}
	}
	for ; i < len(listA); i++ {
		if result := e.tryMatch(point, listA[i]); result != nil {
			results = append(results, result)
			if !listA[i].Continue() {
				return results
			}
		}
	}
	for ; j < len(listB); j++ {
		if result := e.tryMatch(point, listB[j]); result != nil {
			results = append(results, result)
			if !listB[j].Continue() {
				return results
			}
		}
	}
	return results
}

// tryMatch evaluates a single rule and returns a MatchResult if it matches.
func (e *Engine) tryMatch(point core.DataPoint, r core.Rule) *MatchResult {
	if r.Match(point) {
		targets := r.Targets()
		if len(targets) == 0 && r.Target() != "" {
			targets = []string{r.Target()}
		}
		return &MatchResult{
			Rule:      r,
			Targets:   targets,
			Transform: r.Transform(),
		}
	}
	return nil
}

// matchInRules collects matching rules from the given slice until a
// terminal match (Continue() == false). When all rules are terminal, the
// slice contains at most one element.
func (e *Engine) matchInRules(point core.DataPoint, rules []core.Rule) []*MatchResult {
	var results []*MatchResult
	for _, r := range rules {
		if r.Match(point) {
			targets := r.Targets()
			if len(targets) == 0 && r.Target() != "" {
				targets = []string{r.Target()}
			}
			results = append(results, &MatchResult{
				Rule:      r,
				Targets:   targets,
				Transform: r.Transform(),
			})
			if !r.Continue() {
				return results
			}
		}
	}
	return results
}
