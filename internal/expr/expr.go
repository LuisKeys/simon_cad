// Package expr is a safe, bounded arithmetic expression evaluator for CAD-IR
// parameters (e.g. "base.dx * 0.5"). It has no I/O, no assignment, and no loops.
package expr

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

const (
	MaxLength = 512
	MaxDepth  = 48
	maxSteps  = 10000
)

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tIdent
	tOp
)

type token struct {
	kind tokKind
	text string
	num  float64
}

func lex(s string) ([]token, error) {
	if len(s) > MaxLength {
		return nil, fmt.Errorf("expression longer than %d characters", MaxLength)
	}
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			f, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("bad number %q", s[i:j])
			}
			out = append(out, token{kind: tNum, num: f})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '.' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			out = append(out, token{kind: tIdent, text: s[i:j]})
			i = j
		case strings.ContainsRune("+-*/^(),", rune(c)):
			out = append(out, token{kind: tOp, text: string(c)})
			i++
		default:
			return nil, fmt.Errorf("unexpected character %q", c)
		}
	}
	return append(out, token{kind: tEOF}), nil
}

type node interface{}
type (
	numNode   float64
	varNode   string
	unaryNode struct{ x node }
	binNode   struct {
		op   byte
		l, r node
	}
	callNode struct {
		fn   string
		args []node
	}
)

type parser struct {
	toks  []token
	pos   int
	depth int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return fmt.Errorf("expression nested deeper than %d", MaxDepth)
	}
	return nil
}

func (p *parser) expr() (node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text[0]
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
	return l, nil
}

func (p *parser) term() (node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") {
		op := p.next().text[0]
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
	return l, nil
}

func (p *parser) power() (node, error) {
	b, err := p.primary()
	if err != nil {
		return nil, err
	}
	if p.isOp("^") {
		p.next()
		e, err := p.unary() // right associative; allows 2^-1
		if err != nil {
			return nil, err
		}
		return binNode{'^', b, e}, nil
	}
	return b, nil
}

func (p *parser) unary() (node, error) {
	if p.isOp("-") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return unaryNode{x}, nil
	}
	if p.isOp("+") {
		p.next()
		return p.unary()
	}
	return p.power()
}

func (p *parser) primary() (node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	t := p.next()
	switch t.kind {
	case tNum:
		return numNode(t.num), nil
	case tIdent:
		if p.isOp("(") {
			p.next()
			var args []node
			if !p.isOp(")") {
				for {
					a, err := p.expr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.isOp(",") {
						p.next()
						continue
					}
					break
				}
			}
			if !p.isOp(")") {
				return nil, fmt.Errorf("expected ')'")
			}
			p.next()
			return callNode{t.text, args}, nil
		}
		return varNode(t.text), nil
	case tOp:
		if t.text == "(" {
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, fmt.Errorf("expected ')'")
			}
			p.next()
			return e, nil
		}
	}
	return nil, fmt.Errorf("unexpected token in expression")
}

func parse(s string) (node, error) {
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("unexpected trailing input")
	}
	return n, nil
}

// The parsed form of an expression never changes and every rebuild re-evaluates the same few
// strings, so parses are remembered. Trees are immutable once built, so sharing them between
// goroutines is safe. The memory is bounded: a full cache is simply emptied.
const maxCached = 4096

type parsed struct {
	n   node
	err error
}

var (
	parseMu    sync.Mutex
	parseCache = map[string]parsed{}
)

func parseCached(s string) (node, error) {
	parseMu.Lock()
	p, ok := parseCache[s]
	parseMu.Unlock()
	if ok {
		return p.n, p.err
	}
	n, err := parse(s)
	parseMu.Lock()
	if len(parseCache) >= maxCached {
		clear(parseCache)
	}
	parseCache[s] = parsed{n, err}
	parseMu.Unlock()
	return n, err
}

// Check reports whether s is a syntactically valid expression.
func Check(s string) error { _, err := parseCached(s); return err }

// Variables lists the distinct identifiers (excluding function names and constants), in order.
func Variables(s string) []string {
	n, err := parseCached(s)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	var walk func(node)
	walk = func(n node) {
		switch v := n.(type) {
		case varNode:
			if _, isConst := constants[string(v)]; !isConst && !seen[string(v)] {
				seen[string(v)] = true
				out = append(out, string(v))
			}
		case unaryNode:
			walk(v.x)
		case binNode:
			walk(v.l)
			walk(v.r)
		case callNode:
			for _, a := range v.args {
				walk(a)
			}
		}
	}
	walk(n)
	return out
}

// NodeOf returns the node-id part of a "node.param" identifier ("" if it has no dot).
func NodeOf(v string) string {
	if i := strings.IndexByte(v, '.'); i > 0 {
		return v[:i]
	}
	return ""
}

var constants = map[string]float64{"pi": math.Pi}

var funcs = map[string]struct {
	arity int // -1 = variadic (>=1)
	fn    func(a []float64) float64
}{
	"sin":   {1, func(a []float64) float64 { return math.Sin(a[0]) }},
	"cos":   {1, func(a []float64) float64 { return math.Cos(a[0]) }},
	"tan":   {1, func(a []float64) float64 { return math.Tan(a[0]) }},
	"sqrt":  {1, func(a []float64) float64 { return math.Sqrt(a[0]) }},
	"abs":   {1, func(a []float64) float64 { return math.Abs(a[0]) }},
	"round": {1, func(a []float64) float64 { return math.Round(a[0]) }},
	"min": {-1, func(a []float64) float64 {
		m := a[0]
		for _, x := range a[1:] {
			m = math.Min(m, x)
		}
		return m
	}},
	"max": {-1, func(a []float64) float64 {
		m := a[0]
		for _, x := range a[1:] {
			m = math.Max(m, x)
		}
		return m
	}},
}

// Eval evaluates s with variable lookup via env. Unknown variables are an error.
func Eval(s string, env func(name string) (float64, bool)) (float64, error) {
	n, err := parseCached(s)
	if err != nil {
		return 0, err
	}
	steps := 0
	var ev func(node) (float64, error)
	ev = func(n node) (float64, error) {
		if steps++; steps > maxSteps {
			return 0, fmt.Errorf("expression too expensive")
		}
		switch v := n.(type) {
		case numNode:
			return float64(v), nil
		case varNode:
			if c, ok := constants[string(v)]; ok {
				return c, nil
			}
			if f, ok := env(string(v)); ok {
				return f, nil
			}
			return 0, fmt.Errorf("unknown variable %q", string(v))
		case unaryNode:
			x, err := ev(v.x)
			return -x, err
		case binNode:
			l, err := ev(v.l)
			if err != nil {
				return 0, err
			}
			r, err := ev(v.r)
			if err != nil {
				return 0, err
			}
			var res float64
			switch v.op {
			case '+':
				res = l + r
			case '-':
				res = l - r
			case '*':
				res = l * r
			case '/':
				if r == 0 {
					return 0, fmt.Errorf("division by zero")
				}
				res = l / r
			case '^':
				res = math.Pow(l, r)
			}
			if math.IsNaN(res) || math.IsInf(res, 0) {
				return 0, fmt.Errorf("non-finite result")
			}
			return res, nil
		case callNode:
			f, ok := funcs[v.fn]
			if !ok {
				return 0, fmt.Errorf("unknown function %q", v.fn)
			}
			if f.arity >= 0 && len(v.args) != f.arity || f.arity < 0 && len(v.args) == 0 {
				return 0, fmt.Errorf("wrong argument count for %s", v.fn)
			}
			args := make([]float64, len(v.args))
			for i, a := range v.args {
				x, err := ev(a)
				if err != nil {
					return 0, err
				}
				args[i] = x
			}
			res := f.fn(args)
			if math.IsNaN(res) || math.IsInf(res, 0) {
				return 0, fmt.Errorf("non-finite result from %s", v.fn)
			}
			return res, nil
		}
		return 0, fmt.Errorf("bad expression")
	}
	return ev(n)
}
