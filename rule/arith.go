package rule

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// This file implements arithmetic expression evaluation for the `transform`
// rule action using expr-lang/expr. It replaces the former hand-written
// tokenizer/parser (~220 lines) with a thin wrapper (~30 lines).
//
// Supported syntax (identical to before, now powered by expr-lang/expr):
//   value              the original numeric value
//   42, 1.8, -3.14     numeric literals
//   + - * /            arithmetic operators
//   ( )                grouping

// arithCompileEnv is the type hint for compilation.
var arithCompileEnv = map[string]any{
	"value": float64(0),
}

// arithCache caches compiled programs by expression string to avoid
// recompilation overhead on repeated calls with the same expression.
//
// Boundedness: expressions originate from static configuration (the
// transform field of core.RuleConfig), which is a finite set loaded at
// startup/reload. The cache therefore grows to at most one entry per
// distinct configured expression and is never driven by unbounded
// runtime input, so no eviction is needed.
var arithCache sync.Map

// EvalArith parses and evaluates an arithmetic expression against the given
// value. The expression must reference the variable as `value`.
// Compiled programs are cached for reuse across calls with the same expression.
func EvalArith(exprStr string, value float64) (float64, error) {
	if strings.TrimSpace(exprStr) == "" {
		return 0, fmt.Errorf("empty expression")
	}

	// Look up or compile the program
	var program *vm.Program
	if cached, ok := arithCache.Load(exprStr); ok {
		program = cached.(*vm.Program)
	} else {
		p, err := expr.Compile(exprStr, expr.Env(arithCompileEnv), expr.AsFloat64())
		if err != nil {
			return 0, fmt.Errorf("compile arithmetic expression %q: %w", exprStr, err)
		}
		program = p
		arithCache.Store(exprStr, p)
	}

	result, err := expr.Run(program, map[string]any{"value": value})
	if err != nil {
		return 0, fmt.Errorf("eval arithmetic expression %q: %w", exprStr, err)
	}

	f, ok := result.(float64)
	if !ok {
		return 0, fmt.Errorf("expression %q did not produce a float64, got %T: %v", exprStr, result, result)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("expression %q produced an invalid result (%v), likely division by zero", exprStr, f)
	}
	return f, nil
}
