package rule

import (
	"github.com/CoreC-Dev/CoreC/core"
)

// matchMerged evaluates two priority-sorted rule lists in global priority
// order and returns the first match. It uses a two-pointer merge so the
// evaluation order is identical to a single sorted list.
func (e *Engine) matchMerged(point core.DataPoint, listA, listB []core.Rule) *MatchResult {
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
			return result
		}
	}
	for ; i < len(listA); i++ {
		if result := e.tryMatch(point, listA[i]); result != nil {
			return result
		}
	}
	for ; j < len(listB); j++ {
		if result := e.tryMatch(point, listB[j]); result != nil {
			return result
		}
	}
	return nil
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

// matchInRules returns the first matching rule from the given slice.
func (e *Engine) matchInRules(point core.DataPoint, rules []core.Rule) *MatchResult {
	for _, r := range rules {
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
	}
	return nil
}
