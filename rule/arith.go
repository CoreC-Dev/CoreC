package rule

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// This file implements a minimal arithmetic expression evaluator used by the
// `transform` rule action to compute a new value from the original DataPoint
// value (e.g. "value * 1.8 + 32" for Celsius→Fahrenheit conversion).
//
// Supported syntax:
//   value              the original numeric value
//   42, 1.8, -3.14     numeric literals
//   + - * /            arithmetic operators
//   ( )                grouping
//
// The grammar (recursive descent, standard precedence):
//   expr   = term (('+' | '-') term)*
//   term   = factor (('*' | '/') factor)*
//   factor = number | 'value' | '(' expr ')' | ('+' | '-') factor

// EvalArith parses and evaluates an arithmetic expression against the given
// value. The expression must reference the variable as `value`.
func EvalArith(expr string, value float64) (float64, error) {
	tokens, err := tokenizeArith(expr)
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, fmt.Errorf("empty expression")
	}
	p := &arithParser{tokens: tokens}
	result, err := p.parseExpr(value)
	if err != nil {
		return 0, err
	}
	if p.pos < len(p.tokens) {
		return 0, fmt.Errorf("unexpected trailing token %q", p.tokens[p.pos].val)
	}
	return result, nil
}

// ─── Tokenizer ───────────────────────────────────────────────────────

type arithTokenKind int

const (
	atNumber arithTokenKind = iota
	atIdent
	atOp
	atLParen
	atRParen
)

type arithToken struct {
	kind arithTokenKind
	val  string
	num  float64
}

func tokenizeArith(s string) ([]arithToken, error) {
	var tokens []arithToken
	i := 0
	for i < len(s) {
		ch := s[i]
		// skip whitespace
		if ch == ' ' || ch == '\t' {
			i++
			continue
		}
		switch ch {
		case '(':
			tokens = append(tokens, arithToken{kind: atLParen, val: "("})
			i++
		case ')':
			tokens = append(tokens, arithToken{kind: atRParen, val: ")"})
			i++
		case '+', '-', '*', '/':
			tokens = append(tokens, arithToken{kind: atOp, val: string(ch)})
			i++
		default:
			switch {
			case unicode.IsDigit(rune(ch)) || ch == '.':
				// read number
				start := i
				for i < len(s) && (unicode.IsDigit(rune(s[i])) || s[i] == '.') {
					i++
				}
				numStr := s[start:i]
				num, err := strconv.ParseFloat(numStr, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid number %q: %w", numStr, err)
				}
				tokens = append(tokens, arithToken{kind: atNumber, val: numStr, num: num})
			case unicode.IsLetter(rune(ch)):
				// read identifier
				start := i
				for i < len(s) && (unicode.IsLetter(rune(s[i])) || unicode.IsDigit(rune(s[i])) || s[i] == '_') {
					i++
				}
				tokens = append(tokens, arithToken{kind: atIdent, val: s[start:i]})
			default:
				return nil, fmt.Errorf("unexpected character %q", ch)
			}
		}
	}
	return tokens, nil
}

// ─── Parser ──────────────────────────────────────────────────────────

type arithParser struct {
	tokens []arithToken
	pos    int
}

func (p *arithParser) peek() *arithToken {
	if p.pos < len(p.tokens) {
		return &p.tokens[p.pos]
	}
	return nil
}

func (p *arithParser) parseExpr(value float64) (float64, error) {
	left, err := p.parseTerm(value)
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != atOp || (t.val != "+" && t.val != "-") {
			break
		}
		p.pos++
		right, err := p.parseTerm(value)
		if err != nil {
			return 0, err
		}
		if t.val == "+" {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

func (p *arithParser) parseTerm(value float64) (float64, error) {
	left, err := p.parseFactor(value)
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != atOp || (t.val != "*" && t.val != "/") {
			break
		}
		p.pos++
		right, err := p.parseFactor(value)
		if err != nil {
			return 0, err
		}
		if t.val == "*" {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
	}
	return left, nil
}

func (p *arithParser) parseFactor(value float64) (float64, error) {
	t := p.peek()
	if t == nil {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	switch t.kind {
	case atNumber:
		p.pos++
		return t.num, nil
	case atIdent:
		p.pos++
		if strings.EqualFold(t.val, "value") {
			return value, nil
		}
		return 0, fmt.Errorf("unknown variable %q (only 'value' is supported)", t.val)
	case atLParen:
		p.pos++
		inner, err := p.parseExpr(value)
		if err != nil {
			return 0, err
		}
		if p.peek() == nil || p.peek().kind != atRParen {
			return 0, fmt.Errorf("expected ')' after grouped expression")
		}
		p.pos++
		return inner, nil
	case atOp:
		if t.val == "-" {
			p.pos++
			inner, err := p.parseFactor(value)
			if err != nil {
				return 0, err
			}
			return -inner, nil
		}
		if t.val == "+" {
			p.pos++
			return p.parseFactor(value)
		}
		return 0, fmt.Errorf("unexpected operator %q at start of factor", t.val)
	}
	return 0, fmt.Errorf("unexpected token %q", t.val)
}
