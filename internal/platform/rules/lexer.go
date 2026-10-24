package rules

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The lexer reads the tokens of the rules language as the official compiler does, as hidane
// measured it on the emulator (hidane crates/hidane-rules/src/lexer.rs, issues #59 and #61):
//
//   - Whitespace is space, tab, CR, LF and form feed; "//" to the end of the line and "/* */"
//     are comments. Any other character that starts no token (a non-breaking space, '#', '~',
//     a letter outside ASCII) is a "token recognition error" and is dropped.
//   - A path segment is one token: '/' followed by the characters of a URL path segment
//     (A-Z a-z 0-9 - . _ ~ & ' * + : @, and %HH), or by such characters in parentheses,
//     "/(default)". So "4/2" is the number 4 and the segment "/2", a syntax error, while
//     "4 / 2" divides. A comment wins when it is at least as long ("/* … */").
//   - Numbers are decimal: 1, 1.5, 1. and .5. There are no exponents and no hex: "1e3" is 1
//     and the identifier e3.
//   - Identifiers are ASCII letters, digits, '_' and '$'. A '$' alone is the token of "$(x)"
//     in a path, and '@' is a token no rule takes.
//   - Strings are quoted with ' or ", with the escapes \n \r \t \b \f \\ \' \", \xHH, \uHHHH
//     and three octal digits. A raw line break, CR, backspace or form feed, an unknown escape
//     or the end of the file leaves the string unclosed: an unpaired quote, a token of its own
//     that ends there. b'…' and B'…' are bytes. There are no raw strings.
//   - "++" and "--" are tokens, so that the parser refuses them by name.
//
// Keywords are identifiers to the lexer; the parser tells them apart by their text.

// lexKind is the lexical class of a lexeme.
type lexKind uint8

const (
	lexEOF      lexKind = iota
	lexOp               // an operator or a punctuation mark: lexeme.op
	lexIdent            // an identifier or a keyword
	lexInt              // 12
	lexFloat            // 1.5, 1., .5
	lexString           // 'a' or "a"
	lexBytes            // b'a'
	lexUnpaired         // a string without its closing quote, up to where it stops
	lexSegment          // a path segment, "/users" or "/(default)"
	lexDollar           // $, of $(x) in a path
	lexAt               // @, which no rule takes
)

// lexeme is one token read from the source.
type lexeme struct {
	kind lexKind
	op   token // for lexOp
	span
}

// lexError is a character that starts no token.
type lexError struct {
	span
	text string // what was not read, with \n, \r and \t spelled out
}

// lexer reads a whole file at once.
type lexer struct {
	src    string
	pos    int
	tokens []lexeme
	errors []lexError
	// closeAt is the first "*/" at or after closeFrom, or -1 when there is none, so that a file
	// of unclosed "/*" is not searched to its end from each of them.
	closeFrom, closeAt int
}

// lex reads every token of src. The last token is lexEOF, at the end of src.
func lex(src string) ([]lexeme, []lexError) {
	l := &lexer{src: src, closeFrom: -1}
	l.run()
	return l.tokens, l.errors
}

func (l *lexer) run() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f':
			l.pos++
		case c == '/':
			comment, segment := l.commentLen(), lexSegmentLen(l.src[l.pos+1:])
			switch {
			case comment > 0 && comment >= segment:
				l.pos += comment
			case segment > 0:
				l.push(lexSegment, 0, 1+segment)
			default:
				l.push(lexOp, tokSlash, 1)
			}
		case lexIsDigit(c) || c == '.' && l.pos+1 < len(l.src) && lexIsDigit(l.src[l.pos+1]):
			l.number()
		case (c == 'b' || c == 'B') && l.pos+1 < len(l.src) && (l.src[l.pos+1] == '\'' || l.src[l.pos+1] == '"'):
			if n, closed := lexStringLen(l.src[l.pos+1:]); closed {
				l.push(lexBytes, 0, 1+n)
				continue
			}
			// Not bytes: b is an identifier, and the quote starts what follows.
			l.push(lexIdent, 0, 1)
		case c == '\'' || c == '"':
			n, closed := lexStringLen(l.src[l.pos:])
			if closed {
				l.push(lexString, 0, n)
			} else {
				l.push(lexUnpaired, 0, n)
			}
		case lexIsWord(c):
			n := 1
			for l.pos+n < len(l.src) && lexIsWord(l.src[l.pos+n]) {
				n++
			}
			if c == '$' && n == 1 {
				l.push(lexDollar, 0, 1)
			} else {
				l.push(lexIdent, 0, n)
			}
		case c == '@':
			l.push(lexAt, 0, 1)
		default:
			if op, n := l.punctuation(); n > 0 {
				l.push(lexOp, op, n)
			} else {
				l.unrecognized()
			}
		}
	}
	l.tokens = append(l.tokens, lexeme{kind: lexEOF, span: span{len(l.src), len(l.src)}})
}

func (l *lexer) push(kind lexKind, op token, n int) {
	l.tokens = append(l.tokens, lexeme{kind: kind, op: op, span: span{l.pos, l.pos + n}})
	l.pos += n
}

// commentLen returns the length of the comment that starts here, or 0. A block comment that
// is not closed is no comment.
func (l *lexer) commentLen() int {
	rest := l.src[l.pos:]
	switch {
	case strings.HasPrefix(rest, "//"):
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			return i
		}
		return len(rest)
	case strings.HasPrefix(rest, "/*"):
		if at := l.blockClose(l.pos + 2); at >= 0 {
			return at + 2 - l.pos
		}
	}
	return 0
}

// blockClose returns the offset of the first "*/" at or after from, or -1. The offsets asked
// only grow, so each search starts past the last one: the lexer stays linear.
func (l *lexer) blockClose(from int) int {
	if l.closeFrom < 0 || from < l.closeFrom || l.closeAt >= 0 && from > l.closeAt {
		l.closeFrom, l.closeAt = from, -1
		if i := strings.Index(l.src[from:], "*/"); i >= 0 {
			l.closeAt = from + i
		}
	}
	return l.closeAt
}

// number reads 1, 1.5, 1. or .5: a float when it has a dot.
func (l *lexer) number() {
	n := 0
	for l.pos+n < len(l.src) && lexIsDigit(l.src[l.pos+n]) {
		n++
	}
	if l.pos+n < len(l.src) && l.src[l.pos+n] == '.' {
		n++
		for l.pos+n < len(l.src) && lexIsDigit(l.src[l.pos+n]) {
			n++
		}
		l.push(lexFloat, 0, n)
		return
	}
	l.push(lexInt, 0, n)
}

// punctuation returns the operator or punctuation mark that starts here, longest first.
func (l *lexer) punctuation() (token, int) {
	if l.pos+1 < len(l.src) {
		switch l.src[l.pos : l.pos+2] {
		case "&&":
			return tokAnd, 2
		case "||":
			return tokOr, 2
		case "==":
			return tokEQ, 2
		case "!=":
			return tokNE, 2
		case "<=":
			return tokLE, 2
		case ">=":
			return tokGE, 2
		case "++":
			return tokIncrement, 2
		case "--":
			return tokDecrement, 2
		}
	}
	switch l.src[l.pos] {
	case '(':
		return tokLParen, 1
	case ')':
		return tokRParen, 1
	case '[':
		return tokLBrack, 1
	case ']':
		return tokRBrack, 1
	case '{':
		return tokLBrace, 1
	case '}':
		return tokRBrace, 1
	case ',':
		return tokComma, 1
	case '.':
		return tokDot, 1
	case ':':
		return tokColon, 1
	case ';':
		return tokSemicolon, 1
	case '?':
		return tokQuestion, 1
	case '=':
		return tokAssign, 1
	case '!':
		return tokNot, 1
	case '+':
		return tokPlus, 1
	case '-':
		return tokMinus, 1
	case '*':
		return tokStar, 1
	case '%':
		return tokPercent, 1
	case '<':
		return tokLT, 1
	case '>':
		return tokGT, 1
	}
	return tokIllegal, 0
}

// unrecognized records a character that starts no token and skips it. A lone '&' or '|' takes
// the character after it too, which ANTLR consumed looking for the second one.
func (l *lexer) unrecognized() {
	_, n := utf8.DecodeRuneInString(l.src[l.pos:])
	if c := l.src[l.pos]; (c == '&' || c == '|') && l.pos+n < len(l.src) {
		_, m := utf8.DecodeRuneInString(l.src[l.pos+n:])
		n += m
	}
	text := lexSpelled.Replace(l.src[l.pos : l.pos+n])
	l.errors = append(l.errors, lexError{span: span{l.pos, l.pos + n}, text: text})
	l.pos += n
}

// lexSpelled spells line breaks and tabs out, as ANTLR's messages quote tokens.
var lexSpelled = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`)

func lexIsDigit(c byte) bool { return '0' <= c && c <= '9' }

func lexIsWord(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || lexIsDigit(c) || c == '_' || c == '$'
}

func lexIsHex(c byte) bool { return lexIsDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

// lexSegmentLen returns the length of the path segment that follows a '/', without the slash,
// or 0.
func lexSegmentLen(s string) int {
	if strings.HasPrefix(s, "(") {
		if n := lexSegmentChars(s[1:]); n > 0 && 1+n < len(s) && s[1+n] == ')' {
			return n + 2
		}
		return 0
	}
	return lexSegmentChars(s)
}

// lexSegmentChars returns the length of the run of path segment characters at the start of s.
func lexSegmentChars(s string) int {
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case lexIsWord(c) && c != '$' || strings.IndexByte("-.~&'*+:@", c) >= 0:
			i++
		case c == '%' && i+2 < len(s) && lexIsHex(s[i+1]) && lexIsHex(s[i+2]):
			i += 3
		default:
			return i
		}
	}
	return i
}

// lexStringLen returns the length of the string that starts with the quote at s[0], and
// whether it is closed. A string that is not closed ends before the first character it cannot
// take.
func lexStringLen(s string) (int, bool) {
	quote := s[0]
	i := 1
	for i < len(s) {
		switch c := s[i]; {
		case c == quote:
			return i + 1, true
		case c == '\n' || c == '\r' || c == '\b' || c == '\f':
			return i, false
		case c == '\\':
			n := lexEscapeLen(s[i+1:])
			if n == 0 {
				return i, false
			}
			i += 1 + n
		default:
			i++
		}
	}
	return i, false
}

// lexEscapeLen returns the length of the escape after a backslash, or 0 when there is none.
func lexEscapeLen(s string) int {
	hex := func(n int) bool {
		if len(s) <= n {
			return false
		}
		for i := 1; i <= n; i++ {
			if !lexIsHex(s[i]) {
				return false
			}
		}
		return true
	}
	if s == "" {
		return 0
	}
	switch c := s[0]; {
	case strings.IndexByte(`nrtbf\'"`, c) >= 0:
		return 1
	case c == 'x' && hex(2):
		return 3
	case c == 'u' && hex(4):
		return 5
	case '0' <= c && c <= '3' && len(s) > 2 && '0' <= s[1] && s[1] <= '7' && '0' <= s[2] && s[2] <= '7':
		return 3
	}
	return 0
}

// lexUnquote returns the value of a closed string or bytes literal, as written with its quotes
// and its b. Strings are WTF-8 in the engine: \xHH, \uHHHH and octal escapes are code points,
// \u escapes of a surrogate pair make one code point (the compiler reads UTF-16), and a lone
// surrogate keeps its own three bytes, never U+FFFD (production: '\uD83D' == '\uFFFD' is
// false). In bytes, \xHH and octal escapes are a byte each; \u is refused (lexBytesHasU), and
// reads as the low byte of its code point here.
func lexUnquote(lit string) string {
	isBytes := lit[0] == 'b' || lit[0] == 'B'
	body := lit[1 : len(lit)-1]
	if isBytes {
		body = lit[2 : len(lit)-1]
	}
	if strings.IndexByte(body, '\\') < 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			i++
			continue
		}
		e := body[i+1]
		switch e {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '\\', '\'', '"':
			b.WriteByte(e)
		}
		if strings.IndexByte(`nrtbf\'"`, e) >= 0 {
			i += 2
			continue
		}
		var code uint64
		switch e {
		case 'x':
			code, _ = strconv.ParseUint(body[i+2:i+4], 16, 32)
			i += 4
		case 'u':
			code, _ = strconv.ParseUint(body[i+2:i+6], 16, 32)
			i += 6
			if r := rune(code); utf16.IsSurrogate(r) && !isBytes {
				if j := i; j+6 <= len(body) && body[j] == '\\' && body[j+1] == 'u' && lexEscapeLen(body[j+1:]) == 5 {
					low, _ := strconv.ParseUint(body[j+2:j+6], 16, 32)
					if pair := utf16.DecodeRune(r, rune(low)); pair != utf8.RuneError {
						b.WriteRune(pair)
						i += 6
						continue
					}
				}
				b.WriteString(lone(r)) // WTF-8's form (wtf8.go)
				continue
			}
		default:
			code, _ = strconv.ParseUint(body[i+1:i+4], 8, 32)
			i += 4
		}
		if isBytes || code < utf8.RuneSelf {
			b.WriteByte(byte(code))
		} else {
			b.WriteRune(rune(code))
		}
	}
	return b.String()
}

// posCursor converts byte offsets of one source to the positions issues report (issues.go):
// lines end at "\n" alone, so "\r\n" ends one line and a lone "\r" is a character, as ANTLR
// counts them; columns and offsets count code points, as the emulator does (hidane diag.rs).
// It is cheap when the offsets come in order, as sorted issues do.
type posCursor struct {
	src string
	// off is the byte offset reached, and line, col and cp its line, its column and the code
	// points before it.
	off, line, col, cp int
}

func newPosCursor(src string) *posCursor { return &posCursor{src: src, line: 1, col: 1} }

// at returns the position of a span. Offsets outside the source are clamped to it.
func (c *posCursor) at(s span) Position {
	clamp := func(off int) int { return min(max(off, 0), len(c.src)) }
	start, end := clamp(s.start), clamp(s.end)
	end = max(end, start)
	if start < c.off {
		*c = posCursor{src: c.src, line: 1, col: 1}
	}
	for c.off < start {
		r, n := utf8.DecodeRuneInString(c.src[c.off:])
		c.off += n
		c.cp++
		if r == '\n' {
			c.line, c.col = c.line+1, 1
		} else {
			c.col++
		}
	}
	// EndOffset is the span's last code point, inclusive, as the emulator reports it ("foo"
	// at 128 ends at 130); an empty span ends where it starts.
	n := utf8.RuneCountInString(c.src[start:end])
	return Position{Line: c.line, Column: c.col, CurrentOffset: c.cp, EndOffset: c.cp + max(n-1, 0)}
}

// lexBytesHasU reports whether a bytes literal, as written, holds a \u escape, which the
// compiler refuses: "Invalid bytes literal b'\u00ff'." (production).
func lexBytesHasU(lit string) bool {
	for i := 0; i+1 < len(lit); i++ {
		if lit[i] == '\\' {
			if lit[i+1] == 'u' {
				return true
			}
			i++ // the escaped character
		}
	}
	return false
}
