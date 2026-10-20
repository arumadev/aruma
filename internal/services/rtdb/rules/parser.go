package rules

import "fmt"

// The parser is a Pratt parser over JavaScript's expression grammar, cut down to what rules
// allow. The grammar's shape follows Bolt's rules parser (Apache-2.0) in idea only.

// maxExprDepth bounds the nesting of one expression, so a hostile rule cannot exhaust the stack.
const maxExprDepth = 200

type parser struct {
	lx    lexer
	tok   token
	depth int
}

// parseExpr parses one rule expression. A single trailing semicolon is allowed.
func parseExpr(src string) (expr, *exprError) {
	p := &parser{lx: lexer{src: src}}
	if err := p.advance(true); err != nil {
		return nil, err
	}
	if p.tok.kind == tokEOF {
		return nil, errAt(p.tok.pos, "Unexpected end of expression")
	}
	e, err := p.expression(0)
	if err != nil {
		return nil, err
	}
	semicolon := false
	if p.isPunct(";") {
		semicolon = true
		if err := p.advance(true); err != nil {
			return nil, err
		}
	}
	if p.tok.kind != tokEOF {
		if p.isPunct(",") && !semicolon {
			return nil, errAt(e.at(), commaRefused)
		}
		if semicolon || p.tok.nl {
			return nil, errAt(p.tok.pos, "Rule expressions may not contain multiple expressions.")
		}
		return nil, p.unexpected()
	}
	return e, nil
}

func (p *parser) advance(regexOK bool) *exprError {
	t, err := p.lx.next(regexOK)
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *parser) isPunct(s string) bool { return p.tok.kind == tokPunct && p.tok.text == s }

func (p *parser) unexpected() *exprError {
	switch p.tok.kind {
	case tokEOF:
		return errAt(p.tok.pos, "Unexpected end of expression")
	case tokPunct, tokIdent:
		return errAt(p.tok.pos, fmt.Sprintf("Unexpected token '%s'.", p.tok.text))
	default:
		return errAt(p.tok.pos, "Unexpected token.")
	}
}

// expect consumes the punctuator s; after it an operand may follow unless it closes something.
func (p *parser) expect(s string) *exprError {
	if !p.isPunct(s) {
		return p.unexpected()
	}
	return p.advance(s != ")" && s != "]")
}

// Binding powers, after JavaScript's precedence.
const (
	bpCond     = 2
	bpOr       = 4
	bpAnd      = 5
	bpEquality = 8
	bpRelation = 9
	bpAdditive = 11
	bpMultiply = 12
	bpPostfix  = 15
)

var infix = map[string]int{
	"?": bpCond, "||": bpOr, "&&": bpAnd,
	"==": bpEquality, "!=": bpEquality, "===": bpEquality, "!==": bpEquality,
	"<": bpRelation, ">": bpRelation, "<=": bpRelation, ">=": bpRelation,
	"+": bpAdditive, "-": bpAdditive, "*": bpMultiply, "/": bpMultiply, "%": bpMultiply,
	".": bpPostfix, "[": bpPostfix, "(": bpPostfix,
}

// refused names the JavaScript operators that rules leave out, so the error can name them.
var refused = map[string]string{
	"=": "Rule expressions may not contain assignments.",
}

const commaRefused = "Rule expressions may not contain ',' operator."

func init() {
	for _, op := range []string{"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", ">>>=", "**="} {
		refused[op] = "Rule expressions may not contain assignments."
	}
	for _, op := range []string{"&", "|", "^", "<<", ">>", ">>>", "**", "??", "?.", "=>", "++", "--", "..."} {
		refused[op] = fmt.Sprintf("Rule expressions may not contain '%s' operator.", op)
	}
}

func (p *parser) expression(minBP int) (expr, *exprError) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxExprDepth {
		return nil, errAt(p.tok.pos, "Expression is nested too deeply.")
	}
	left, err := p.prefix()
	if err != nil {
		return nil, err
	}
	for {
		t := p.tok
		if t.kind == tokIdent && (t.text == "in" || t.text == "instanceof") {
			return nil, errAt(left.at(), fmt.Sprintf("Rule expressions may not contain '%s' operator.", t.text))
		}
		if t.kind != tokPunct {
			return left, nil
		}
		if msg, ok := refused[t.text]; ok {
			return nil, errAt(left.at(), msg)
		}
		bp, ok := infix[t.text]
		if !ok || bp <= minBP {
			return left, nil
		}
		switch t.text {
		case ".":
			if err := p.advance(false); err != nil {
				return nil, err
			}
			if p.tok.kind != tokIdent {
				return nil, p.unexpected()
			}
			left = &member{pos: left.at(), obj: left, name: p.tok.text}
			if err := p.advance(false); err != nil {
				return nil, err
			}
		case "[":
			if err := p.advance(true); err != nil {
				return nil, err
			}
			key, err := p.expression(0)
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			m := &member{pos: left.at(), obj: left, literal: true}
			if s, ok := key.(*literal); ok {
				if str, ok := s.val.(string); ok {
					m.name = str
				} else {
					m.key = key
				}
			} else {
				m.key = key
			}
			left = m
		case "(":
			if err := p.advance(true); err != nil {
				return nil, err
			}
			c := &call{pos: left.at(), callee: left}
			for !p.isPunct(")") {
				arg, err := p.expression(bpCond - 1)
				if err != nil {
					return nil, err
				}
				c.args = append(c.args, arg)
				if p.isPunct(",") {
					if err := p.advance(true); err != nil {
						return nil, err
					}
					continue
				}
				if !p.isPunct(")") {
					return nil, p.unexpected()
				}
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			left = c
		case "?":
			if err := p.advance(true); err != nil {
				return nil, err
			}
			yes, err := p.expression(0)
			if err != nil {
				return nil, err
			}
			if err := p.expect(":"); err != nil {
				return nil, err
			}
			no, err := p.expression(bpCond - 1)
			if err != nil {
				return nil, err
			}
			left = &cond{pos: left.at(), test: left, yes: yes, no: no}
		default:
			if err := p.advance(true); err != nil {
				return nil, err
			}
			right, err := p.expression(bp)
			if err != nil {
				return nil, err
			}
			op := t.text
			switch op { // == is ===, and != is !==, in rules
			case "===":
				op = "=="
			case "!==":
				op = "!="
			}
			left = &binary{pos: left.at(), op: op, l: left, r: right}
		}
	}
}

// prefix parses an operand: a literal, a variable, a parenthesised expression or a unary
// operator applied to an operand.
func (p *parser) prefix() (expr, *exprError) {
	t := p.tok
	switch t.kind {
	case tokEOF:
		return nil, p.unexpected()
	case tokNum:
		return &literal{pos: t.pos, val: t.num}, p.advance(false)
	case tokStr:
		return &literal{pos: t.pos, val: t.str}, p.advance(false)
	case tokRegex:
		return &regexLit{pos: t.pos, src: "/" + t.text + "/" + t.str}, p.advance(false)
	case tokIdent:
		switch t.text {
		case "true", "false":
			return &literal{pos: t.pos, val: t.text == "true"}, p.advance(false)
		case "null":
			return &literal{pos: t.pos, val: nil}, p.advance(false)
		case "this":
			return nil, errAt(t.pos, "Rule expressions may not contain 'this' keyword.")
		case "typeof", "void", "delete", "new":
			return nil, errAt(t.pos, fmt.Sprintf("Rule expressions may not contain '%s' operator.", t.text))
		case "var", "let", "const", "function", "if", "else", "return", "for", "while", "do",
			"switch", "case", "break", "continue", "throw", "try", "catch", "finally", "class",
			"import", "export", "yield", "await", "with", "debugger", "in", "instanceof":
			return nil, p.unexpected()
		}
		return &ident{pos: t.pos, name: t.text}, p.advance(false)
	}
	switch t.text {
	case "(":
		if err := p.advance(true); err != nil {
			return nil, err
		}
		e, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		if p.isPunct(",") {
			return nil, errAt(e.at(), commaRefused)
		}
		return e, p.expect(")")
	case "[":
		if err := p.advance(true); err != nil {
			return nil, err
		}
		a := &arrayLit{pos: t.pos}
		for !p.isPunct("]") {
			el, err := p.expression(bpCond - 1)
			if err != nil {
				return nil, err
			}
			a.elems = append(a.elems, el)
			if p.isPunct(",") {
				if err := p.advance(true); err != nil {
					return nil, err
				}
				continue
			}
			if !p.isPunct("]") {
				return nil, p.unexpected()
			}
		}
		return a, p.expect("]")
	case "!", "-":
		if err := p.advance(true); err != nil {
			return nil, err
		}
		p.depth++
		defer func() { p.depth-- }()
		if p.depth > maxExprDepth {
			return nil, errAt(p.tok.pos, "Expression is nested too deeply.")
		}
		x, err := p.prefixOperand()
		if err != nil {
			return nil, err
		}
		return &unary{pos: t.pos, op: t.text, x: x}, nil
	case "+", "~":
		return nil, errAt(t.pos, fmt.Sprintf("Rule expressions may not contain '%s' operator.", t.text))
	case "++":
		return nil, errAt(t.pos, "Invalid increment operand.")
	case "--":
		return nil, errAt(t.pos, "Invalid decrement operand.")
	case "{":
		return nil, errAt(t.pos, "Rule expressions may not contain object literals.")
	}
	return nil, p.unexpected()
}

// prefixOperand parses the operand of a unary operator: an operand with its member accesses
// and calls, which bind tighter than the operator.
func (p *parser) prefixOperand() (expr, *exprError) {
	return p.expression(bpPostfix - 1)
}
