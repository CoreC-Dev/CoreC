package rule

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// This file implements the rule expression engine using expr-lang/expr.
// It replaces the former hand-written tokenizer/parser/AST (~560 lines)
// with a thin translation layer (~100 lines) that converts the CoreC DSL
// to expr-lang/expr syntax, then compiles and evaluates via the library.
//
// Supported DSL syntax (translated to expr-lang/expr built-in operators):
//   field == 'value'        string equality           (native)
//   field != 'value'        string inequality          (native)
//   field =~ 'regex'        regex match (RE2)         → field matches 'regex'
//   field !~ 'regex'        regex non-match            → not (field matches 'regex')
//   field contains 'substr' substring containment      (native operator)
//   field suffix 'suffix'   suffix match              → field endsWith 'suffix'
//   field prefix 'prefix'   prefix match              → field startsWith 'prefix'
//   value > 90              numeric comparison          (native)
//   value == true           boolean equality            (native)
//   value == 42             numeric equality            (native)
//   value in 50..100        numeric range check        → value >= 50 && value <= 100
//   expr && expr            logical AND                 (native)
//   expr || expr            logical OR                  (native)
//   !expr                   logical NOT                 (native)
//   (expr)                  grouping                    (native)
//
// Fields: driver, device, group, tag, quality, type, value

// ─── exprNode interface (kept for backward compatibility) ──────────

type exprNode interface {
	eval(point core.DataPoint) bool
}

// exprLangNode wraps a compiled expr-lang/expr program.
type exprLangNode struct {
	program *vm.Program
}

func (n *exprLangNode) eval(point core.DataPoint) bool {
	env := pointEnv(point)
	result, err := expr.Run(n.program, env)
	if err != nil {
		return false
	}
	b, ok := result.(bool)
	return ok && b
}

// pointEnv builds the evaluation environment from a DataPoint.
// The raw point.Value is passed as-is so that boolean comparisons
// (value == true) and numeric comparisons (value > 50) both work
// via expr-lang/expr's runtime type dispatch.
func pointEnv(point core.DataPoint) map[string]any {
	return map[string]any{
		"driver":  point.Driver,
		"device":  point.Device,
		"group":   point.Group,
		"tag":     point.Tag,
		"quality": point.Quality.String(),
		"type":    point.Type.String(),
		"value":   point.Value,
	}
}

// ─── DSL → expr-lang/expr syntax translation ────────────────────────

// translateExpr converts the CoreC DSL to expr-lang/expr syntax.
// Only the operators not native to expr-lang/expr are translated;
// everything else (==, !=, >, <, >=, <=, &&, ||, !) passes through unchanged.
//
// Each translation wraps the result in parentheses to preserve the
// original DSL's operator precedence, where unary ! applies to the
// entire comparison expression (e.g. !tag =~ 'x' means !(tag =~ 'x')).
// In expr-lang/expr, ! has higher precedence than comparison operators,
// so without parentheses !tag matches 'x' would parse as (!tag) matches 'x'.
func translateExpr(s string) string {
	// field =~ 'regex'  →  (field matches 'regex')
	s = reRegexMatch.ReplaceAllString(s, "($1 matches '$2')")
	// field !~ 'regex'  →  not (field matches 'regex')
	s = reRegexNonMatch.ReplaceAllString(s, "not ($1 matches '$2')")
	// field contains 'substr'  →  (field contains 'substr')
	s = reContains.ReplaceAllString(s, "($1 contains '$2')")
	// field suffix 'suffix'  →  (field endsWith 'suffix')
	s = reSuffix.ReplaceAllString(s, "($1 endsWith '$2')")
	// field prefix 'prefix'  →  (field startsWith 'prefix')
	s = rePrefix.ReplaceAllString(s, "($1 startsWith '$2')")
	// value in 50..100  →  (value >= 50 && value <= 100)
	// reRange is applied last and only outside single-quoted string literals,
	// to avoid corrupting regex patterns that happen to contain "word in N..M".
	s = replaceRangeOutsideStrings(s)
	return s
}

// replaceRangeOutsideStrings applies the range translation (in A..B → (>= A && <= B))
// only to matches that are NOT inside single-quoted string literals.
func replaceRangeOutsideStrings(s string) string {
	matches := reRange.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}

	// Positions of all single-quoted string literals in the (already partially translated) string.
	stringRanges := reStringLit.FindAllStringIndex(s, -1)

	var buf strings.Builder
	lastEnd := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		buf.WriteString(s[lastEnd:start])

		insideString := false
		for _, sr := range stringRanges {
			if start >= sr[0] && end <= sr[1] {
				insideString = true
				break
			}
		}

		if insideString {
			buf.WriteString(s[start:end]) // keep original
		} else {
			field := s[m[2]:m[3]]
			lo := s[m[4]:m[5]]
			hi := s[m[6]:m[7]]
			fmt.Fprintf(&buf, "(%s >= %s && %s <= %s)", field, lo, field, hi)
		}
		lastEnd = end
	}
	buf.WriteString(s[lastEnd:])
	return buf.String()
}

var (
	reRegexMatch    = regexp.MustCompile(`(\w+)\s*=~\s*'([^']*)'`)
	reRegexNonMatch = regexp.MustCompile(`(\w+)\s*!~\s*'([^']*)'`)
	reContains      = regexp.MustCompile(`(\w+)\s+contains\s+'([^']*)'`)
	reSuffix        = regexp.MustCompile(`(\w+)\s+suffix\s+'([^']*)'`)
	rePrefix        = regexp.MustCompile(`(\w+)\s+prefix\s+'([^']*)'`)
	reRange         = regexp.MustCompile(`(\w+)\s+in\s+(-?\d+(?:\.\d+)?)\.\.(-?\d+(?:\.\d+)?)`)
	reStringLit     = regexp.MustCompile(`'[^']*'`)
)

// ─── Compiler ───────────────────────────────────────────────────────

// compileExpr parses a DSL expression string into an evaluable node.
// Uses expr.AllowUndefinedVariables() so that the `value` field (which
// can be bool, int, float64, etc.) is type-checked at runtime rather
// than compile time.
func compileExpr(exprStr string) (exprNode, error) {
	translated := translateExpr(exprStr)

	if strings.TrimSpace(translated) == "" {
		return nil, fmt.Errorf("empty expression")
	}

	program, err := expr.Compile(translated, expr.AsBool(), expr.AllowUndefinedVariables(), expr.DisableBuiltin("type"))
	if err != nil {
		return nil, fmt.Errorf("compile expression %q (translated: %q): %w", exprStr, translated, err)
	}

	return &exprLangNode{program: program}, nil
}

// allNode matches everything.
type allNode struct{}

func (n *allNode) eval(_ core.DataPoint) bool { return true }
