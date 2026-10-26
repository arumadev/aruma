package wire

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The front end reads a body with protobuf's JSON stream parser, not a strict JSON reader: a
// trailing comma closes an object or an array, a string may be single-quoted and a key unquoted.
// Its errors quote up to 20 bytes either side of the place it stopped, with a caret under it:
//
//	Invalid JSON payload received. Parsing terminated before end of input.
//	{"documents": []} x
//	                  ^
//
// A body that ends too soon is reported with an empty quote and the caret alone, "Unexpected end
// of string. " before what was expected (f1-wire-errors.json json.syntax_error,
// json.trailing_garbage). parseJSON reproduces that parser's grammar and messages.

// maxJSONDepth bounds the nesting of a body's objects and arrays, so a hostile body cannot
// exhaust the stack. A Firestore value nests at most 20 maps (three levels each), well inside it.
const maxJSONDepth = 1000

// jsonContext is how many bytes of the body an error quotes either side of its place.
const jsonContext = 20

// parseJSON reads a body into its tree, or answers the front end's syntax error.
func parseJSON(body []byte) (*jsonNode, error) {
	p := &jsonParser{s: body}
	n, ok := p.value(0)
	if !ok {
		return nil, p.err
	}
	p.space()
	if p.pos < len(p.s) {
		p.fail("Parsing terminated before end of input.")
		return nil, p.err
	}
	return n, nil
}

type jsonParser struct {
	s   []byte
	pos int
	err *Error
}

// fail records the error at the parser's place.
func (p *jsonParser) fail(msg string) {
	begin, end := max(p.pos-jsonContext, 0), min(p.pos+jsonContext, len(p.s))
	p.err = InvalidArgument("Invalid JSON payload received. " + msg + "\n" +
		string(p.s[begin:end]) + "\n" + strings.Repeat(" ", p.pos-begin) + "^")
}

// unknown records an error where the parser could not tell the next token: at the end of the
// body, "Unexpected end of string." with an empty quote, as the stream parser reports it once it
// has consumed everything.
func (p *jsonParser) unknown(msg string) {
	if p.pos >= len(p.s) {
		p.err = InvalidArgument("Invalid JSON payload received. Unexpected end of string. " + msg + "\n\n^")
		return
	}
	p.fail(msg)
}

func (p *jsonParser) space() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

// literal reports whether the body continues with word, and consumes it.
func (p *jsonParser) literal(word string) bool {
	if bytes.HasPrefix(p.s[p.pos:], []byte(word)) {
		p.pos += len(word)
		return true
	}
	return false
}

// value reads one value at nesting depth.
func (p *jsonParser) value(depth int) (*jsonNode, bool) {
	p.space()
	if p.pos >= len(p.s) {
		p.unknown("Expected a value.")
		return nil, false
	}
	switch c := p.s[p.pos]; {
	case c == '{' || c == '[':
		if depth >= maxJSONDepth {
			p.fail("Message too deep.")
			return nil, false
		}
		if c == '{' {
			return p.object(depth + 1)
		}
		return p.array(depth + 1)
	case c == '"' || c == '\'':
		s, ok := p.str()
		return &jsonNode{kind: jsonString, s: s}, ok
	case c == '-' || isDigit(c):
		return p.number()
	case p.literal("true"):
		return &jsonNode{kind: jsonBool, b: true}, true
	case p.literal("false"):
		return &jsonNode{kind: jsonBool}, true
	case p.literal("null"):
		return &jsonNode{kind: jsonNull}, true
	}
	p.unknown("Expected a value.")
	return nil, false
}

func (p *jsonParser) object(depth int) (*jsonNode, bool) {
	n := &jsonNode{kind: jsonObject}
	p.pos++ // {
	for {
		p.space()
		if p.pos >= len(p.s) {
			p.unknown("Expected an object key or }.")
			return nil, false
		}
		var key string
		switch c := p.s[p.pos]; {
		case c == '}': // also after a trailing comma
			p.pos++
			return n, true
		case c == '"' || c == '\'':
			s, ok := p.str()
			if !ok {
				return nil, false
			}
			key = s
		case isKeyStart(c):
			start := p.pos
			for p.pos < len(p.s) && isKeyByte(p.s[p.pos]) {
				p.pos++
			}
			key = string(p.s[start:p.pos])
		default:
			p.fail("Expected an object key or }.")
			return nil, false
		}
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != ':' {
			p.unknownOr("Expected : between key:value pair.")
			return nil, false
		}
		p.pos++
		v, ok := p.value(depth)
		if !ok {
			return nil, false
		}
		n.members = append(n.members, jsonMember{key, v})
		p.space()
		switch {
		case p.pos >= len(p.s):
			p.unknown("Expected , or } after key:value pair.")
			return nil, false
		case p.s[p.pos] == ',':
			p.pos++
		case p.s[p.pos] == '}':
			p.pos++
			return n, true
		default:
			p.fail("Expected , or } after key:value pair.")
			return nil, false
		}
	}
}

func (p *jsonParser) array(depth int) (*jsonNode, bool) {
	n := &jsonNode{kind: jsonArray}
	p.pos++ // [
	for {
		p.space()
		if p.pos >= len(p.s) {
			p.unknown("Expected a value or ] within an array.")
			return nil, false
		}
		if p.s[p.pos] == ']' { // also after a trailing comma
			p.pos++
			return n, true
		}
		v, ok := p.value(depth)
		if !ok {
			return nil, false
		}
		n.elems = append(n.elems, v)
		p.space()
		switch {
		case p.pos >= len(p.s):
			p.unknown("Expected , or ] after array value.")
			return nil, false
		case p.s[p.pos] == ',':
			p.pos++
		case p.s[p.pos] == ']':
			p.pos++
			return n, true
		default:
			p.fail("Expected , or ] after array value.")
			return nil, false
		}
	}
}

// unknownOr is unknown at the end of the body, else fail.
func (p *jsonParser) unknownOr(msg string) {
	if p.pos >= len(p.s) {
		p.unknown(msg)
		return
	}
	p.fail(msg)
}

func isKeyStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$'
}

func isKeyByte(c byte) bool { return isKeyStart(c) || isDigit(c) }

// number reads a number's text, which the field it lands in parses.
func (p *jsonParser) number() (*jsonNode, bool) {
	start := p.pos
	for p.pos < len(p.s) && strings.IndexByte("0123456789+-.eE", p.s[p.pos]) >= 0 {
		p.pos++
	}
	text := string(p.s[start:p.pos])
	if _, err := strconv.ParseFloat(text, 64); err != nil && !errors.Is(err, strconv.ErrRange) {
		p.pos = start
		p.fail("Unable to parse number.")
		return nil, false
	}
	return &jsonNode{kind: jsonNumber, s: text}, true
}

// str reads a string in double or single quotes. Bytes that are not UTF-8 become a space, and an
// escaped lone surrogate U+FFFD, as production reads them (f2-non-utf8.json
// rest.commit_name_raw_ff, rest.commit_name_escaped_lone_surrogate): no text off the wire that is
// not UTF-8 goes further.
func (p *jsonParser) str() (string, bool) {
	start := p.pos
	quote := p.s[p.pos]
	p.pos++
	var b strings.Builder
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == quote:
			p.pos++
			return validUTF8(b.String()), true
		case c != '\\':
			b.WriteByte(c)
			p.pos++
			continue
		}
		if p.pos+1 >= len(p.s) {
			break
		}
		esc := p.s[p.pos+1]
		if r, ok := simpleEscapes[esc]; ok {
			b.WriteByte(r)
			p.pos += 2
			continue
		}
		if esc != 'u' {
			p.pos++
			p.fail("Invalid escape sequence.")
			return "", false
		}
		r, ok := p.hex4(p.pos + 2)
		if !ok {
			p.fail("Illegal hex string.")
			return "", false
		}
		p.pos += 6
		if utf16.IsSurrogate(r) && p.pos+1 < len(p.s) && p.s[p.pos] == '\\' && p.s[p.pos+1] == 'u' {
			if r2, ok := p.hex4(p.pos + 2); ok {
				if pair := utf16.DecodeRune(r, r2); pair != utf8.RuneError {
					r = pair
					p.pos += 6
				}
			}
		}
		b.WriteRune(r)
	}
	p.pos = start
	p.unknownOr("Closing quote expected in string.")
	return "", false
}

var simpleEscapes = map[byte]byte{'"': '"', '\'': '\'', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

// hex4 reads the four hex digits at i.
func (p *jsonParser) hex4(i int) (rune, bool) {
	if i+4 > len(p.s) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.s[i:i+4]), 16, 32)
	return rune(n), err == nil
}

// validUTF8 is s with each byte that is not UTF-8 read as a space, as production reads a REST
// query parameter or JSON string (f2-non-utf8.json rest.create_document_id_ff,
// rest.commit_name_raw_ff, rest.get_mask_ff).
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && n <= 1 {
			b.WriteByte(' ')
		} else {
			b.WriteString(s[:n])
		}
		s = s[n:]
	}
	return b.String()
}
