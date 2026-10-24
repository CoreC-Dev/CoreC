package rule

import (
	"github.com/CoreC-Dev/CoreC/core"
)

// sequencer is an optional interface rule wrappers implement to expose
// their build-order position, used as the tie-break when two rules share
// a priority: same-priority rules evaluate in
// configuration order, with universal and driver rules interleaved. Rules
// that do not implement it fall back to the previous behaviour (listA
// first on equal priority).
type sequencer interface {
	Seq() int
}

// ruleSeq returns the build-order position of r, or 0 when r does not
// expose one.
func ruleSeq(r core.Rule) int {
	if s, ok := r.(sequencer); ok {
		return s.Seq()
	}
	return 0
}

// matchMerged evaluates two priority-sorted rule lists in global priority
// order and collects all matching rules until a terminal match (a rule
// with Continue() == false). It uses a two-pointer merge so the evaluation
// order is identical to a single sorted list. On equal priority the
// build-order sequence breaks the tie (B7-1): the rule that appeared first
// in the configuration evaluates first, regardless of which list it came
// from — a universal rule no longer implicitly shadows a same-priority
// driver rule.
//
// When every rule is terminal (Continue() == false, the default), the
// first match is returned and the slice contains exactly one element —
// the former first-match-wins behavior.
func (e *Engine) matchMerged(point core.DataPoint, listA, listB []core.Rule) []*MatchResult {
	var results []*MatchResult
	i, j := 0, 0
	for i < len(listA) && j < len(listB) {
		var r core.Rule
		pa, pb := listA[i].Priority(), listB[j].Priority()
		if pa < pb || (pa == pb && ruleSeq(listA[i]) <= ruleSeq(listB[j])) {
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
