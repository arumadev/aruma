package rules

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A rule expression is a small subset of JavaScript. The lexer reads JavaScript's tokens, so
// that what the subset leaves out (assignments, bitwise operators, this, typeof...) fails with
// a message that names it, as Firebase's does, instead of a bare syntax error.

type tokKind uint8

const (
	tokEOF tokKind = iota
	tokNum
	tokStr
	tokIdent
	tokRegex
	tokPunct
)

type token struct {
	kind tokKind
	pos  int    // byte offset in the expression
	text string // the punctuator, the identifier, or the regex body
	num  float64
	str  string // a string literal's value, or a regex's flags
	// nl reports a line break before the token: two expressions on separate lines are the
	// "multiple expressions" Firebase refuses.
	nl bool
}

// exprError is an error at a byte offset of the expression.
type exprError struct {
	pos int
	msg string
}

func errAt(pos int, msg string) *exprError { return &exprError{pos: pos, msg: msg} }

type lexer struct {
	src string
	pos int
}

// punctuators lists JavaScript's operators, longest first so that "===" wins over "==".
var punctuators = []string{
	">>>=", "===", "!==", ">>>", "<<=", ">>=", "**=", "...",
	"==", "!=", "<=", ">=", "&&", "||", "++", "--", "<<", ">>", "**", "+=", "-=", "*=", "/=", "%=",
	"&=", "|=", "^=", "=>", "??", "?.",
	"(", ")", "[", "]", "{", "}", ".", ",", ";", ":", "?", "!", "~", "+", "-", "*", "/", "%",
	"<", ">", "=", "&", "|", "^",
}

// next reads the next token. regexOK is true where an operand is expected, the one place where
// a slash starts a regular expression literal rather than a division.
func (l *lexer) next(regexOK bool) (token, *exprError) {
	nl, err := l.space()
	if err != nil {
		return token{}, err
	}
	start := l.pos
	if l.pos >= len(l.src) {
		return token{kind: tokEOF, pos: start, nl: nl}, nil
	}
	c := l.src[l.pos]
	switch {
	case c >= '0' && c <= '9' || c == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]):
		t, err := l.number()
		t.nl = nl
		return t, err
	case c == '"' || c == '\'':
		s, err := l.stringLit(c)
		return token{kind: tokStr, pos: start, str: s, nl: nl}, err
	case c == '/' && regexOK:
		body, flags, err := l.regex()
		return token{kind: tokRegex, pos: start, text: body, str: flags, nl: nl}, err
	case c == '`':
		return token{}, errAt(start, "illegal character")
	}
	r, size := utf8.DecodeRuneInString(l.src[l.pos:])
	if isIdentStart(r) {
		l.pos += size
		for l.pos < len(l.src) {
			r, size := utf8.DecodeRuneInString(l.src[l.pos:])
			if !isIdentPart(r) {
				break
			}
			l.pos += size
		}
		return token{kind: tokIdent, pos: start, text: l.src[start:l.pos], nl: nl}, nil
	}
	for _, p := range punctuators {
		if strings.HasPrefix(l.src[l.pos:], p) {
			l.pos += len(p)
			return token{kind: tokPunct, pos: start, text: p, nl: nl}, nil
		}
	}
	return token{}, errAt(start, "illegal character")
}

// space skips whitespace and comments and reports whether it crossed a line break.
func (l *lexer) space() (bool, *exprError) {
	nl := false
	for l.pos < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])
		switch {
		case r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			nl = true
			l.pos += size
		case unicode.IsSpace(r) || r == 0xfeff:
			l.pos += size
		case strings.HasPrefix(l.src[l.pos:], "//"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
		case strings.HasPrefix(l.src[l.pos:], "/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				return nl, errAt(len(l.src), "unterminated comment")
			}
			if strings.ContainsAny(l.src[l.pos:l.pos+2+end], "\n\r") {
				nl = true
			}
			l.pos += end + 4
		default:
			return nl, nil
		}
	}
	return nl, nil
}

func (l *lexer) number() (token, *exprError) {
	start := l.pos
	var v float64
	if strings.HasPrefix(l.src[l.pos:], "0x") || strings.HasPrefix(l.src[l.pos:], "0X") {
		l.pos += 2
		for l.pos < len(l.src) && isHex(l.src[l.pos]) {
			l.pos++
		}
		n, err := strconv.ParseUint(l.src[start+2:l.pos], 16, 64)
		if err != nil {
			return token{}, errAt(start, "Invalid number.")
		}
		v = float64(n)
	} else {
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		if l.pos < len(l.src) && l.src[l.pos] == '.' {
			l.pos++
			for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
				l.pos++
			}
		}
		if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
			l.pos++
			if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
				l.pos++
			}
			digits := l.pos
			for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
				l.pos++
			}
			if digits == l.pos {
				return token{}, errAt(start, "Invalid number.")
			}
		}
		f, err := strconv.ParseFloat(l.src[start:l.pos], 64)
		if err != nil && !isRangeErr(err) {
			return token{}, errAt(start, "Invalid number.")
		}
		v = f
	}
	if l.pos < len(l.src) {
		if r, _ := utf8.DecodeRuneInString(l.src[l.pos:]); isIdentStart(r) || isDigit(l.src[l.pos]) {
			return token{}, errAt(l.pos, "illegal character")
		}
	}
	return token{kind: tokNum, pos: start, num: v}, nil
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// stringLit reads a quoted string with JavaScript's escapes.
func (l *lexer) stringLit(quote byte) (string, *exprError) {
	start := l.pos
	l.pos++
	var b strings.Builder
	for {
		if l.pos >= len(l.src) {
			return "", errAt(start, "unterminated string literal")
		}
		c := l.src[l.pos]
		switch {
		case c == quote:
			l.pos++
			return b.String(), nil
		case c == '\n' || c == '\r':
			return "", errAt(start, "unterminated string literal")
		case c == '\\':
			l.pos++
			if l.pos >= len(l.src) {
				return "", errAt(start, "unterminated string literal")
			}
			e := l.src[l.pos]
			l.pos++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '0':
				b.WriteByte(0)
			case '\n': // a line continuation
			case 'x', 'u':
				n := 2
				if e == 'u' {
					n = 4
				}
				if l.pos+n > len(l.src) {
					return "", errAt(l.pos-2, "invalid escape sequence")
				}
				cp, err := strconv.ParseUint(l.src[l.pos:l.pos+n], 16, 32)
				if err != nil {
					return "", errAt(l.pos-2, "invalid escape sequence")
				}
				l.pos += n
				r := rune(cp)
				// A surrogate pair written as two \u escapes is one character.
				if r >= 0xd800 && r < 0xdc00 && strings.HasPrefix(l.src[l.pos:], `\u`) && l.pos+6 <= len(l.src) {
					if lo, err := strconv.ParseUint(l.src[l.pos+2:l.pos+6], 16, 32); err == nil && lo >= 0xdc00 && lo < 0xe000 {
						r = (r-0xd800)<<10 + (rune(lo) - 0xdc00) + 0x10000
						l.pos += 6
					}
				}
				b.WriteRune(r)
			default:
				// Any other escaped character stands for itself, as in JavaScript.
				l.pos--
				r, size := utf8.DecodeRuneInString(l.src[l.pos:])
				b.WriteRune(r)
				l.pos += size
			}
		default:
			b.WriteByte(c)
			l.pos++
		}
	}
}

// regex reads /body/flags. The body ends at the first slash that is neither escaped nor inside
// a character class.
func (l *lexer) regex() (string, string, *exprError) {
	start := l.pos
	l.pos++
	inClass := false
	for {
		if l.pos >= len(l.src) || l.src[l.pos] == '\n' || l.src[l.pos] == '\r' {
			return "", "", errAt(start, "unterminated regular expression literal")
		}
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.pos += 2
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			body := l.src[start+1 : l.pos]
			l.pos++
			fstart := l.pos
			for l.pos < len(l.src) {
				r, size := utf8.DecodeRuneInString(l.src[l.pos:])
				if !isIdentPart(r) {
					break
				}
				l.pos += size
			}
			return body, l.src[fstart:l.pos], nil
		}
		l.pos++
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

func isIdentStart(r rune) bool { return r == '$' || r == '_' || unicode.IsLetter(r) }

func isIdentPart(r rune) bool { return isIdentStart(r) || unicode.IsDigit(r) }
