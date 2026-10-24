package rules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// A small reader of conditions made of literals, enough to run the review's probes of production
// (probes_data_test.go) through the library without S1-A's parser or S1-C's interpreter. It reads
// what the probes write: literals (ints, floats, strings and bytes with production's escapes,
// lists, maps), calls of functions, namespaced functions and methods, x.f, x[i], x[i:j], the
// unary and binary operators by the precedence of the docs, is, in, &&, || and ?:, with the
// error semantics of ADR-021 §2.3. It is a test's tool, not the language's grammar.

type probeCase struct {
	id, expr, outcome, kind, msg string
}

// probeNode is an expression of the reader.
type probeNode interface{}

type (
	pLit   struct{ v Value }
	pIdent struct{ name string }
	pList  struct{ elems []probeNode }
	pMap   struct{ keys, vals []probeNode }
	pField struct {
		x    probeNode
		name string
	}
	pIndex struct{ x, i probeNode }
	pSlice struct{ x, lo, hi probeNode }
	pCall  struct {
		fn   probeNode // pIdent or pField
		args []probeNode
	}
	pUnary struct {
		op token
		x  probeNode
	}
	pBinary struct {
		op   token
		x, y probeNode
	}
	pIs struct {
		x   probeNode
		typ string
	}
	pCond struct{ test, yes, no probeNode }
)

type probeLexeme struct {
	kind string // num, str, bytes, ident, op
	text string
	v    Value
}

// lexProbe splits a condition into lexemes.
func lexProbe(src string) ([]probeLexeme, error) {
	var out []probeLexeme
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c >= '0' && c <= '9':
			j := i
			float := false
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' && j+1 < len(src) && src[j+1] >= '0' && src[j+1] <= '9') {
				if src[j] == '.' {
					float = true
				}
				j++
			}
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
				float = true
				j++
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			text := src[i:j]
			if float {
				f, err := strconv.ParseFloat(text, 64)
				if err != nil {
					return nil, err
				}
				out = append(out, probeLexeme{kind: "num", v: Float(f)})
			} else {
				n, err := strconv.ParseInt(text, 10, 64)
				if err != nil {
					return nil, err
				}
				out = append(out, probeLexeme{kind: "num", v: Int(n)})
			}
			i = j
		case c == '\'' || c == '"' || (c == 'b' || c == 'B') && i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '"'):
			isBytes := c == 'b' || c == 'B'
			if isBytes {
				i++
			}
			s, n, err := unquoteProbe(src[i:], isBytes)
			if err != nil {
				return nil, err
			}
			if isBytes {
				out = append(out, probeLexeme{kind: "bytes", v: Bytes([]byte(s))})
			} else {
				out = append(out, probeLexeme{kind: "str", v: String(s)})
			}
			i += n
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			out = append(out, probeLexeme{kind: "ident", text: src[i:j]})
			i = j
		default:
			for _, op := range []string{"&&", "||", "==", "!=", "<=", ">=", "<", ">", "!", "+", "-", "*", "/", "%", "(", ")", "[", "]", "{", "}", ",", ".", ":", "?"} {
				if strings.HasPrefix(src[i:], op) {
					out = append(out, probeLexeme{kind: "op", text: op})
					i += len(op)
					goto next
				}
			}
			return nil, fmt.Errorf("unexpected %q", src[i:])
		next:
		}
	}
	return out, nil
}

// unquoteProbe reads a quoted literal at the start of s with production's escapes (the probes
// of string-literals-and-regex): \x and octal are code points (bytes in a bytes literal), \u one
// UTF-16 unit, a lone one in WTF-8; it returns the value and the bytes read.
func unquoteProbe(s string, isBytes bool) (string, int, error) {
	q := s[0]
	var units []rune // UTF-16 units and characters, in order, for a string
	var raw []byte
	add := func(r rune) {
		if isBytes {
			if r < 0x100 && !(r >= 0x80 && !isBytes) {
				raw = append(raw, byte(r))
			} else {
				raw = utf8.AppendRune(raw, r)
			}
			return
		}
		units = append(units, r)
	}
	i := 1
	for i < len(s) && s[i] != q {
		if s[i] != '\\' {
			r, n := utf8.DecodeRuneInString(s[i:])
			if isBytes {
				raw = append(raw, s[i:i+n]...)
			} else {
				units = append(units, r)
			}
			i += n
			continue
		}
		i++
		switch e := s[i]; e {
		case 'n':
			add('\n')
			i++
		case 'r':
			add('\r')
			i++
		case 't':
			add('\t')
			i++
		case 'b':
			add('\b')
			i++
		case 'f':
			add('\f')
			i++
		case 'x':
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", 0, err
			}
			add(rune(v))
			i += 3
		case 'u':
			v, err := strconv.ParseUint(s[i+1:i+5], 16, 16)
			if err != nil {
				return "", 0, err
			}
			add(rune(v))
			i += 5
		default:
			if e >= '0' && e <= '7' {
				j := i
				for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
					j++
				}
				v, _ := strconv.ParseUint(s[i:j], 8, 16)
				add(rune(v))
				i = j
			} else {
				r, n := utf8.DecodeRuneInString(s[i:])
				add(r)
				i += n
			}
		}
	}
	if i >= len(s) {
		return "", 0, errors.New("unterminated literal")
	}
	if isBytes {
		return string(raw), i + 1, nil
	}
	// Join the units: a high surrogate and a low one are a pair, a lone one stays lone (WTF-8).
	var w wtf8Builder
	for _, r := range units {
		w.add(string(appendWTF8(nil, r)))
	}
	return w.String(), i + 1, nil
}

type probeParser struct {
	lx  []probeLexeme
	pos int
}

func (p *probeParser) peek() probeLexeme {
	if p.pos < len(p.lx) {
		return p.lx[p.pos]
	}
	return probeLexeme{kind: "eof"}
}

func (p *probeParser) isOp(op string) bool {
	l := p.peek()
	return l.kind == "op" && l.text == op || l.kind == "ident" && l.text == op && (op == "in" || op == "is")
}

func (p *probeParser) expect(op string) error {
	if !p.isOp(op) {
		return fmt.Errorf("want %q at %d", op, p.pos)
	}
	p.pos++
	return nil
}

func (p *probeParser) expr() (probeNode, error) {
	test, err := p.binary(0)
	if err != nil || !p.isOp("?") {
		return test, err
	}
	p.pos++
	yes, err := p.expr()
	if err != nil {
		return nil, err
	}
	if err := p.expect(":"); err != nil {
		return nil, err
	}
	no, err := p.expr()
	return pCond{test, yes, no}, err
}

// The binary levels, loosest first (rules-layout.md, from the docs).
var probeLevels = [][]string{{"||"}, {"&&"}, {"==", "!="}, {"is"}, {"in"}, {"<", "<=", ">", ">="}, {"+", "-"}, {"*", "/", "%"}}

var probeTokens = map[string]token{
	"||": tokOr, "&&": tokAnd, "==": tokEQ, "!=": tokNE, "in": tokIn, "<": tokLT, "<=": tokLE,
	">": tokGT, ">=": tokGE, "+": tokPlus, "-": tokMinus, "*": tokStar, "/": tokSlash, "%": tokPercent,
}

func (p *probeParser) binary(level int) (probeNode, error) {
	if level == len(probeLevels) {
		return p.unary()
	}
	x, err := p.binary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		matched := ""
		for _, op := range probeLevels[level] {
			if p.isOp(op) {
				matched = op
			}
		}
		if matched == "" {
			return x, nil
		}
		p.pos++
		if matched == "is" {
			typ := p.peek()
			p.pos++
			x = pIs{x, typ.text}
			continue
		}
		y, err := p.binary(level + 1)
		if err != nil {
			return nil, err
		}
		x = pBinary{probeTokens[matched], x, y}
	}
}

func (p *probeParser) unary() (probeNode, error) {
	switch {
	case p.isOp("!"):
		p.pos++
		x, err := p.unary()
		return pUnary{tokNot, x}, err
	case p.isOp("-"):
		p.pos++
		x, err := p.unary()
		return pUnary{tokMinus, x}, err
	}
	return p.postfix()
}

func (p *probeParser) args(close string) ([]probeNode, error) {
	var out []probeNode
	for !p.isOp(close) {
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		if !p.isOp(close) {
			if err := p.expect(","); err != nil {
				return nil, err
			}
		}
	}
	p.pos++
	return out, nil
}

func (p *probeParser) postfix() (probeNode, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			p.pos++
			name := p.peek().text
			p.pos++
			x = pField{x, name}
		case p.isOp("("):
			p.pos++
			args, err := p.args(")")
			if err != nil {
				return nil, err
			}
			x = pCall{x, args}
		case p.isOp("["):
			p.pos++
			var lo, hi probeNode
			if !p.isOp(":") {
				if lo, err = p.expr(); err != nil {
					return nil, err
				}
			}
			if p.isOp(":") {
				p.pos++
				if !p.isOp("]") {
					if hi, err = p.expr(); err != nil {
						return nil, err
					}
				}
				if err := p.expect("]"); err != nil {
					return nil, err
				}
				x = pSlice{x, lo, hi}
				continue
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			x = pIndex{x, lo}
		default:
			return x, nil
		}
	}
}

func (p *probeParser) primary() (probeNode, error) {
	l := p.peek()
	p.pos++
	switch l.kind {
	case "num", "str", "bytes":
		return pLit{l.v}, nil
	case "ident":
		switch l.text {
		case "true":
			return pLit{Bool(true)}, nil
		case "false":
			return pLit{Bool(false)}, nil
		case "null":
			return pLit{Null()}, nil
		}
		return pIdent{l.text}, nil
	case "op":
		switch l.text {
		case "(":
			x, err := p.expr()
			if err != nil {
				return nil, err
			}
			return x, p.expect(")")
		case "[":
			elems, err := p.args("]")
			return pList{elems}, err
		case "{":
			var keys, vals []probeNode
			for !p.isOp("}") {
				k, err := p.expr()
				if err != nil {
					return nil, err
				}
				if err := p.expect(":"); err != nil {
					return nil, err
				}
				v, err := p.expr()
				if err != nil {
					return nil, err
				}
				keys, vals = append(keys, k), append(vals, v)
				if !p.isOp("}") {
					if err := p.expect(","); err != nil {
						return nil, err
					}
				}
			}
			p.pos++
			return pMap{keys, vals}, nil
		}
	}
	return nil, fmt.Errorf("unexpected %+v", l)
}

func parseProbe(src string) (probeNode, error) {
	lx, err := lexProbe(src)
	if err != nil {
		return nil, err
	}
	p := &probeParser{lx: lx}
	x, err := p.expr()
	if err == nil && p.pos != len(lx) {
		err = fmt.Errorf("trailing input at %d", p.pos)
	}
	return x, err
}

// compileProbe compiles the literal patterns of matches, split and replace, as the checker does.
func compileProbe(rx *regexCache, n probeNode) error {
	var walk func(probeNode) error
	walk = func(n probeNode) error {
		switch x := n.(type) {
		case pCall:
			if f, ok := x.fn.(pField); ok && (f.name == "matches" || f.name == "split" || f.name == "replace") && len(x.args) > 0 {
				if l, ok := x.args[0].(pLit); ok && l.v.kind == KindString {
					if err := compileLiteralRegex(rx, l.v.str()); err != nil {
						return err
					}
				}
			}
			if err := walk(x.fn); err != nil {
				return err
			}
			for _, a := range x.args {
				if err := walk(a); err != nil {
					return err
				}
			}
		case pField:
			return walk(x.x)
		case pIndex:
			if err := walk(x.x); err != nil {
				return err
			}
			return walk(x.i)
		case pSlice:
			for _, y := range []probeNode{x.x, x.lo, x.hi} {
				if y != nil {
					if err := walk(y); err != nil {
						return err
					}
				}
			}
		case pList:
			for _, y := range x.elems {
				if err := walk(y); err != nil {
					return err
				}
			}
		case pMap:
			for i := range x.keys {
				if err := walk(x.keys[i]); err != nil {
					return err
				}
				if err := walk(x.vals[i]); err != nil {
					return err
				}
			}
		case pUnary:
			return walk(x.x)
		case pBinary:
			if err := walk(x.x); err != nil {
				return err
			}
			return walk(x.y)
		case pIs:
			return walk(x.x)
		case pCond:
			for _, y := range []probeNode{x.test, x.yes, x.no} {
				if err := walk(y); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(n)
}

// evalProbe evaluates n, an error being a value until && or || absorbs it (ADR-021 §2.3).
func evalProbe(m *meter, n probeNode) (Value, error) {
	switch x := n.(type) {
	case pLit:
		return x.v, nil
	case pIdent:
		return Value{}, errorf("Property %s is undefined on object.", x.name)
	case pList:
		vals, err := evalProbes(m, x.elems)
		if err != nil {
			return Value{}, err
		}
		return buildList(m, vals)
	case pMap:
		keys, err := evalProbes(m, x.keys)
		if err != nil {
			return Value{}, err
		}
		vals, err := evalProbes(m, x.vals)
		if err != nil {
			return Value{}, err
		}
		return buildMap(m, keys, vals)
	case pField:
		v, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		return selectField(m, v, x.name)
	case pIndex:
		v, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		i, err := evalProbe(m, x.i)
		if err != nil {
			return Value{}, err
		}
		return selectIndex(m, v, i)
	case pSlice:
		v, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		var lo, hi *Value
		for _, b := range []struct {
			n probeNode
			p **Value
		}{{x.lo, &lo}, {x.hi, &hi}} {
			if b.n != nil {
				bv, err := evalProbe(m, b.n)
				if err != nil {
					return Value{}, err
				}
				*b.p = &bv
			}
		}
		return selectSlice(m, v, lo, hi)
	case pCall:
		args, err := evalProbes(m, x.args)
		if err != nil {
			return Value{}, err
		}
		switch f := x.fn.(type) {
		case pIdent:
			return callFunction(m, f.name, args)
		case pField:
			if ns, ok := f.x.(pIdent); ok && isLibraryNamespace(ns.name) {
				return callFunction(m, ns.name+"."+f.name, args)
			}
			recv, err := evalProbe(m, f.x)
			if err != nil {
				return Value{}, err
			}
			return callMethod(m, recv, f.name, args)
		}
		return Value{}, errorf("not callable")
	case pUnary:
		v, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		return unary(m, x.op, v)
	case pBinary:
		if x.op == tokAnd || x.op == tokOr {
			return logicProbe(m, x)
		}
		a, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		b, err := evalProbe(m, x.y)
		if err != nil {
			return Value{}, err
		}
		return binary(m, x.op, a, b)
	case pIs:
		v, err := evalProbe(m, x.x)
		if err != nil {
			return Value{}, err
		}
		return typeIs(v, x.typ)
	case pCond:
		t, err := evalProbe(m, x.test)
		if err != nil {
			return Value{}, err
		}
		if t.kind != KindBool {
			return Value{}, errType(t.kind.String(), "bool")
		}
		if t.boolean() {
			return evalProbe(m, x.yes)
		}
		return evalProbe(m, x.no)
	}
	return Value{}, errorf("unknown node %T", n)
}

func evalProbes(m *meter, ns []probeNode) ([]Value, error) {
	out := make([]Value, len(ns))
	for i, n := range ns {
		v, err := evalProbe(m, n)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// logicProbe is && and || (ADR-021 §2.3): a decided left side short-circuits; a left side that
// is an error or not a bool yields to a deciding right side, and stays the error otherwise.
func logicProbe(m *meter, x pBinary) (Value, error) {
	decides := x.op == tokOr // || is decided by true, && by false
	asBool := func(v Value, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		if v.kind != KindBool {
			return false, errType(v.kind.String(), "bool")
		}
		return v.boolean(), nil
	}
	a, errA := asBool(evalProbe(m, x.x))
	if errA == nil && a == decides {
		return Bool(decides), nil
	}
	b, errB := asBool(evalProbe(m, x.y))
	if errB == nil && b == decides {
		return Bool(decides), nil
	}
	if errA != nil {
		return Value{}, errA
	}
	if errB != nil {
		return Value{}, errB
	}
	return Bool(!decides), nil
}

// The review's probes of production, run through the library: each must answer as production
// did, allow, deny, or the same error.
func TestReviewProbes(t *testing.T) {
	agree := 0
	for _, c := range reviewProbes {
		got, detail := runProbe(c)
		want := c.outcome
		ok := got == want
		if ok && want == "error" && c.msg != "" && detail != c.msg {
			ok = false
		}
		if !ok {
			t.Errorf("%s: %s\n\tgot %s %q\n\twant %s %q", c.id, c.expr, got, detail, want, c.msg)
			continue
		}
		agree++
	}
	t.Logf("%d of %d probes of production agree", agree, len(reviewProbes))
}

// runProbe returns allow, deny or error, with the error's text.
func runProbe(c probeCase) (string, string) {
	n, err := parseProbe(c.expr)
	if err != nil {
		return "unreadable", err.Error()
	}
	lim := DefaultLimits()
	rx := newRegexCache(&lim)
	if err := compileProbe(rx, n); err != nil {
		return "error", err.Error()
	}
	m := newMeter(&lim, NewInflight(lim.InflightBytes), rx)
	v, err := evalProbe(m, n)
	var ee *evalError
	switch {
	case errors.As(err, &ee):
		if c.kind == "request" {
			return "error", "" // production's internal error has no text to compare
		}
		return "error", ee.msg
	case err != nil:
		return "abort", err.Error()
	case v.kind == KindBool && v.boolean():
		return "allow", ""
	}
	return "deny", ""
}

// The reader itself: it reads what it should, and tells allow, deny and errors apart.
func TestProbeReader(t *testing.T) {
	for _, c := range []struct{ expr, want, msg string }{
		{"1 + 2 * 3 == 7 && -(1) < 0", "allow", ""},
		{"[1, 2][1:] == [2] && {'a': [1]}.a[0] == 1", "allow", ""},
		{`'😀'.size() == 2 && '😀' == '😀' && 'a\x41\101' == 'aAA'`, "allow", ""},
		{`b'\xff'.size() == 1`, "allow", ""},
		{"1 == 2", "deny", ""},
		{"(1 / 0 == 1) || true", "allow", ""},
		{"1 / 0 == 1", "error", "Divide by zero error."},
		{"math.abs(-1) is int ? 'x'.matches('x') : false", "allow", ""},
		{"'a'.matches('(')", "error", "Invalid regular expression pattern. Pattern: (."},
	} {
		got, msg := runProbe(probeCase{expr: c.expr})
		if got != c.want || msg != c.msg {
			t.Errorf("%s: %s %q, want %s %q", c.expr, got, msg, c.want, c.msg)
		}
	}
}
