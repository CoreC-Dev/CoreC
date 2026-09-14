package rule

import (
	"math"
	"testing"
)

func TestEvalArith(t *testing.T) {
	tests := []struct {
		expr  string
		value float64
		want  float64
	}{
		{"value", 42, 42},
		{"value * 1.8 + 32", 100, 212},
		{"value * 1.8 + 32", 0, 32},
		{"(value - 0) * 100 / 65535", 32767.5, 50},
		{"value + 10", 5, 15},
		{"value - 10", 5, -5},
		{"value * 2", 3.5, 7},
		{"value / 2", 7, 3.5},
		{"-value", 5, -5},
		{"value + 1 * 2", 10, 12},   // precedence: 10 + (1*2)
		{"(value + 1) * 2", 10, 22}, // grouping
		{"3.14", 0, 3.14},
		{"value * value", 4, 16},
	}
	for _, tt := range tests {
		got, err := EvalArith(tt.expr, tt.value)
		if err != nil {
			t.Errorf("EvalArith(%q, %v) error: %v", tt.expr, tt.value, err)
			continue
		}
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("EvalArith(%q, %v) = %v, want %v", tt.expr, tt.value, got, tt.want)
		}
	}
}

func TestEvalArithErrors(t *testing.T) {
	errorCases := []string{
		"",           // empty
		"value +",    // incomplete
		"value / 0",  // division by zero
		"foo",        // unknown variable
		"value +",    // trailing operator
		"(value + 1", // unclosed paren
		"value @ 2",  // invalid char
	}
	for _, expr := range errorCases {
		_, err := EvalArith(expr, 1)
		if err == nil {
			t.Errorf("EvalArith(%q) expected error, got nil", expr)
		}
	}
}
