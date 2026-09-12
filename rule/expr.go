package rule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// This file implements a lightweight expression parser for rule matching.
// It replaces cel-go (~5MB) with ~300 lines of zero-dependency Go code.
//
// Supported syntax:
//   field == 'value'        string equality
//   field != 'value'        string inequality
//   field =~ 'regex'        regex match (RE2)
//   field !~ 'regex'        regex non-match
//   field contains 'substr' substring containment
//   field suffix 'suffix'   suffix match
//   field prefix 'prefix'   prefix match
//   value > 90              numeric comparison (>, <, >=, <=)
//   value == true           boolean equality
//   value == 42             numeric equality
//   value in 50..100        numeric range check
//   expr && expr            logical AND
//   expr || expr            logical OR
//   !expr                   logical NOT
//   (expr)                  grouping
//
// Fields: driver, device, group, tag, quality, type, value

// ─── AST Nodes ───────────────────────────────────────────────────────

type exprNode interface {
	eval(point core.DataPoint) bool
}

type orNode struct{ left, right exprNode }

func (n *orNode) eval(p core.DataPoint) bool { return n.left.eval(p) || n.right.eval(p) }

type andNode struct{ left, right exprNode }

func (n *andNode) eval(p core.DataPoint) bool { return n.left.eval(p) && n.right.eval(p) }

type notNode struct{ inner exprNode }

func (n *notNode) eval(p core.DataPoint) bool { return !n.inner.eval(p) }

type cmpNode struct {
	field   string
	op      string
	strVal  string
	numVal  float64
	boolVal bool
	valType int // 0=string, 1=number, 2=bool
}

func (n *cmpNode) eval(point core.DataPoint) bool {
	switch n.op {
	case "==":
		return n.equals(point)
	case "!=":
		return !n.equals(point)
	default: // >, <, >=, <=
		return n.compare(point)
	}
}

func (n *cmpNode) equals(point core.DataPoint) bool {
	switch n.field {
	case "driver":
		return point.Driver == n.strVal
	case "device":
		return point.Device == n.strVal
	case "group":
		return point.Group == n.strVal
	case "tag":
		return point.Tag == n.strVal
	case "quality":
		return point.Quality.String() == n.strVal
	case "type":
		return point.Type.String() == n.strVal
	case "value":
		switch n.valType {
		case 2: // bool
			b, ok := point.Value.(bool)
			return ok && b == n.boolVal
		case 1: // number
			return util.ToFloat64(point.Value) == n.numVal
		default: // string
			return fmt.Sprintf("%v", point.Value) == n.strVal
		}
	}
	return false
}

func (n *cmpNode) compare(point core.DataPoint) bool {
	if n.field != "value" {
		return false // only value supports numeric comparison
	}
	val := util.ToFloat64(point.Value)
	switch n.op {
	case ">":
		return val > n.numVal
	case "<":
		return val < n.numVal
	case ">=":
		return val >= n.numVal
	case "<=":
		return val <= n.numVal
	}
	return false
}

// ─── Type-Specific Match Nodes (P1) ──────────────────────────────────

// regexNode matches a string field against a compiled RE2 regex.
type regexNode struct {
	field  string
	re     *regexp.Regexp
	negate bool
}

func (n *regexNode) eval(point core.DataPoint) bool {
	s := fieldString(point, n.field)
	matched := n.re.MatchString(s)
	if n.negate {
		return !matched
	}
	return matched
}

// containsNode checks if a string field contains a substring.
type containsNode struct {
	field  string
	substr string
}

func (n *containsNode) eval(point core.DataPoint) bool {
	return strings.Contains(fieldString(point, n.field), n.substr)
}

// suffixNode checks if a string field ends with a suffix.
type suffixNode struct {
	field  string
	suffix string
}

func (n *suffixNode) eval(point core.DataPoint) bool {
	return strings.HasSuffix(fieldString(point, n.field), n.suffix)
}

// prefixNode checks if a string field starts with a prefix.
type prefixNode struct {
	field  string
	prefix string
}

func (n *prefixNode) eval(point core.DataPoint) bool {
	return strings.HasPrefix(fieldString(point, n.field), n.prefix)
}

// rangeNode checks if a numeric value falls within [lo, hi].
type rangeNode struct {
	field string
	lo    float64
	hi    float64
}

func (n *rangeNode) eval(point core.DataPoint) bool {
	if n.field != "value" {
		return false
	}
	val := util.ToFloat64(point.Value)
	return val >= n.lo && val <= n.hi
}

// fieldString extracts a string field value from a DataPoint.
func fieldString(point core.DataPoint, field string) string {
	switch field {
	case "driver":
		return point.Driver
	case "device":
		return point.Device
	case "group":
		return point.Group
	case "tag":
		return point.Tag
	case "quality":
		return point.Quality.String()
	case "type":
		return point.Type.String()
	case "value":
		return fmt.Sprintf("%v", point.Value)
	}
	return ""
}

// ─── Tokenizer ───────────────────────────────────────────────────────

type tokenType int

const (
	tkEOF tokenType = iota
	tkIdent
	tkString
	tkNumber
	tkBool
	tkOp // ==, !=, >, <, >=, <=, =~, !~
	tkAnd
	tkOr
	tkNot
	tkLParen
	tkRParen
)

type token struct {
	typ tokenType
	val string
}

func tokenize(s string) []token {
	var tokens []token
	i := 0
	for i < len(s) {
		// skip whitespace
		for i < len(s) && unicode.IsSpace(rune(s[i])) {
			i++
		}
		if i >= len(s) {
			break
		}

		ch := s[i]
		switch {
		case ch == '(':
			tokens = append(tokens, token{tkLParen, "("})
			i++
		case ch == ')':
			tokens = append(tokens, token{tkRParen, ")"})
			i++
		case ch == '!':
			switch {
			case i+1 < len(s) && s[i+1] == '=':
				tokens = append(tokens, token{tkOp, "!="})
				i += 2
			case i+1 < len(s) && s[i+1] == '~':
				tokens = append(tokens, token{tkOp, "!~"})
				i += 2
			default:
				tokens = append(tokens, token{tkNot, "!"})
				i++
			}
		case ch == '=' && i+1 < len(s) && s[i+1] == '=':
			tokens = append(tokens, token{tkOp, "=="})
			i += 2
		case ch == '=' && i+1 < len(s) && s[i+1] == '~':
			tokens = append(tokens, token{tkOp, "=~"})
			i += 2
		case ch == '>':
			if i+1 < len(s) && s[i+1] == '=' {
				tokens = append(tokens, token{tkOp, ">="})
				i += 2
			} else {
				tokens = append(tokens, token{tkOp, ">"})
				i++
			}
		case ch == '<':
			if i+1 < len(s) && s[i+1] == '=' {
				tokens = append(tokens, token{tkOp, "<="})
				i += 2
			} else {
				tokens = append(tokens, token{tkOp, "<"})
				i++
			}
		case ch == '&' && i+1 < len(s) && s[i+1] == '&':
			tokens = append(tokens, token{tkAnd, "&&"})
			i += 2
		case ch == '|' && i+1 < len(s) && s[i+1] == '|':
			tokens = append(tokens, token{tkOr, "||"})
			i += 2
		case ch == '\'' || ch == '"':
			quote := ch
			j := i + 1
			for j < len(s) && s[j] != quote {
				j++
			}
			if j < len(s) {
				tokens = append(tokens, token{tkString, s[i+1 : j]})
				i = j + 1
			} else {
				tokens = append(tokens, token{tkString, s[i+1:]})
				i = len(s)
			}
		default:
			// identifier, number, or boolean
			j := i
			for j < len(s) && !unicode.IsSpace(rune(s[j])) &&
				s[j] != '(' && s[j] != ')' &&
				s[j] != '=' && s[j] != '!' && s[j] != '>' && s[j] != '<' &&
				s[j] != '&' && s[j] != '|' && s[j] != '\'' && s[j] != '"' {
				j++
			}
			word := s[i:j]
			i = j

			if word == "true" || word == "false" {
				tokens = append(tokens, token{tkBool, word})
			} else if _, err := strconv.ParseFloat(word, 64); err == nil {
				tokens = append(tokens, token{tkNumber, word})
			} else {
				tokens = append(tokens, token{tkIdent, word})
			}
		}
	}
	return tokens
}

// ─── Parser (recursive descent) ──────────────────────────────────────
//
// Grammar:
//   expr      → or_expr
//   or_expr   → and_expr ('||' and_expr)*
//   and_expr  → not_expr ('&&' not_expr)*
//   not_expr  → '!' not_expr | atom
//   atom      → '(' expr ')' | field op literal | field keyword literal

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return token{tkEOF, ""}
}

func (p *parser) next() token {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) parseExpr() (exprNode, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (exprNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tkOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &orNode{left, right}
	}
	return left, nil
}

func (p *parser) parseAnd() (exprNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().typ == tkAnd {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &andNode{left, right}
	}
	return left, nil
}

func (p *parser) parseNot() (exprNode, error) {
	if p.peek().typ == tkNot {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &notNode{inner}, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (exprNode, error) {
	t := p.peek()

	// Parenthesized expression
	if t.typ == tkLParen {
		p.next()
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().typ != tkRParen {
			return nil, fmt.Errorf("expected ')' but got %q", p.peek().val)
		}
		p.next()
		return expr, nil
	}

	// Comparison: field op literal  or  field keyword literal
	if t.typ != tkIdent {
		return nil, fmt.Errorf("expected field name but got %q", t.val)
	}
	p.next() // consume field
	field := t.val

	next := p.peek()

	// Check for keyword operators: contains, suffix, prefix, in
	if next.typ == tkIdent {
		switch next.val {
		case "contains":
			p.next()
			return p.parseStringOp(field, "contains")
		case "suffix":
			p.next()
			return p.parseStringOp(field, "suffix")
		case "prefix":
			p.next()
			return p.parseStringOp(field, "prefix")
		case "in":
			p.next()
			return p.parseRange(field)
		}
	}

	// Standard operator: ==, !=, >, <, >=, <=, =~, !~
	if next.typ != tkOp {
		return nil, fmt.Errorf("expected operator after %q but got %q", field, next.val)
	}
	p.next() // consume operator
	op := next.val

	// Regex operators
	if op == "=~" || op == "!~" {
		return p.parseRegex(field, op == "!~")
	}

	// Standard comparison
	val := p.peek()
	node := &cmpNode{field: field, op: op}

	switch val.typ {
	case tkString:
		node.strVal = val.val
		node.valType = 0
	case tkNumber:
		num, _ := strconv.ParseFloat(val.val, 64)
		node.numVal = num
		node.valType = 1
	case tkBool:
		node.boolVal = val.val == "true"
		node.valType = 2
	default:
		return nil, fmt.Errorf("expected value after %q %q but got %q", field, op, val.val)
	}
	p.next()

	return node, nil
}

// parseRegex handles field =~ 'pattern' and field !~ 'pattern'
func (p *parser) parseRegex(field string, negate bool) (exprNode, error) {
	val := p.peek()
	if val.typ != tkString {
		return nil, fmt.Errorf("expected regex string after %q but got %q", field, val.val)
	}
	p.next()
	re, err := regexp.Compile(val.val)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", val.val, err)
	}
	return &regexNode{field: field, re: re, negate: negate}, nil
}

// parseStringOp handles field contains/suffix/prefix 'value'
func (p *parser) parseStringOp(field, op string) (exprNode, error) {
	val := p.peek()
	if val.typ != tkString {
		return nil, fmt.Errorf("expected string after %q %s but got %q", field, op, val.val)
	}
	p.next()
	switch op {
	case "contains":
		return &containsNode{field: field, substr: val.val}, nil
	case "suffix":
		return &suffixNode{field: field, suffix: val.val}, nil
	case "prefix":
		return &prefixNode{field: field, prefix: val.val}, nil
	}
	return nil, fmt.Errorf("unknown string op: %s", op)
}

// parseRange handles field in lo..hi
func (p *parser) parseRange(field string) (exprNode, error) {
	val := p.peek()
	p.next()

	// Support both "50..100" (single token) and "50" ".." "100" (multi-token)
	rangeStr := val.val
	if val.typ == tkNumber {
		// Check if next token is ".." or "..100"
		next := p.peek()
		if next.typ == tkIdent && strings.HasPrefix(next.val, "..") {
			p.next()
			rangeStr = val.val + next.val
		}
	}

	// Split on ".."
	parts := strings.SplitN(rangeStr, "..", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range syntax %q, expected lo..hi", rangeStr)
	}
	lo, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid range lower bound %q: %w", parts[0], err)
	}
	hi, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid range upper bound %q: %w", parts[1], err)
	}
	return &rangeNode{field: field, lo: lo, hi: hi}, nil
}

// compileExpr parses an expression string into an evaluable AST.
func compileExpr(expr string) (exprNode, error) {
	tokens := tokenize(expr)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty expression")
	}
	p := &parser{tokens: tokens}
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.tokens) {
		return nil, fmt.Errorf("unexpected trailing token %q", p.tokens[p.pos].val)
	}
	return node, nil
}

// ensure unused import doesn't break
var _ = strings.TrimSpace
