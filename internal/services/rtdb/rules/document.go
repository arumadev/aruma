package rules

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The rules document is JSON as Firebase reads it, which is looser than encoding/json: comments
// (// and /* */) and trailing commas are accepted (emulator 4.11.2, 2026-10-20). Every value keeps
// its offset in the document, and every string the offset of each decoded byte, so an error
// inside a rule expression can name its line and column in the file the developer edits.

type jsonKind uint8

const (
	jsonObject jsonKind = iota
	jsonArray
	jsonString
	jsonNumber
	jsonBool
	jsonNull
)

type jsonValue struct {
	kind jsonKind
	off  int // offset of the value's first byte in the document
	end  int // offset of an object's closing brace
	str  string
	// offs maps each byte of str to its offset in the document, plus one entry for the end, so
	// escapes such as \" or é do not shift the positions reported inside an expression.
	offs    []int
	boolean bool
	members []jsonMember
	elems   []*jsonValue
}

type jsonMember struct {
	key    string
	keyOff int
	val    *jsonValue
}

// maxDocumentDepth bounds the nesting of the document. Data paths are at most 32 keys deep, so
// rules deeper than that can never apply; the bound only keeps the reader's recursion small.
const maxDocumentDepth = 64

type docReader struct {
	src []byte
	pos int
}

func (r *docReader) fail(off int, msg string) *Error {
	line, col := position(r.src, off)
	return &Error{Line: line, Column: col, Msg: msg}
}

// readDocument parses the whole document into one value.
func readDocument(src []byte) (*jsonValue, error) {
	r := &docReader{src: src}
	if err := r.space(); err != nil {
		return nil, err
	}
	if r.pos >= len(src) {
		return nil, r.fail(r.pos, "Empty JSON object")
	}
	v, err := r.value(0)
	if err != nil {
		return nil, err
	}
	if err := r.space(); err != nil {
		return nil, err
	}
	if r.pos < len(src) {
		return nil, r.fail(r.pos, "Unexpected text after the rules object.")
	}
	return v, nil
}

// space skips whitespace and comments.
func (r *docReader) space() error {
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			r.pos++
		case c == '/' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '/':
			for r.pos < len(r.src) && r.src[r.pos] != '\n' {
				r.pos++
			}
		case c == '/' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '*':
			start := r.pos
			r.pos += 2
			for {
				if r.pos+1 >= len(r.src) {
					return r.fail(start, "Unterminated comment.")
				}
				if r.src[r.pos] == '*' && r.src[r.pos+1] == '/' {
					r.pos += 2
					break
				}
				r.pos++
			}
		default:
			return nil
		}
	}
	return nil
}

func (r *docReader) value(depth int) (*jsonValue, error) {
	if depth > maxDocumentDepth {
		return nil, r.fail(r.pos, "Rules are nested too deeply.")
	}
	if r.pos >= len(r.src) {
		return nil, r.fail(r.pos, "Unexpected EOF.")
	}
	start := r.pos
	switch c := r.src[r.pos]; {
	case c == '{':
		return r.object(depth)
	case c == '[':
		return r.array(depth)
	case c == '"':
		s, offs, err := r.str()
		if err != nil {
			return nil, err
		}
		return &jsonValue{kind: jsonString, off: start, str: s, offs: offs}, nil
	case c == '-' || c >= '0' && c <= '9':
		return r.number()
	default:
		for _, lit := range []struct {
			word string
			v    *jsonValue
		}{
			{"true", &jsonValue{kind: jsonBool, boolean: true}},
			{"false", &jsonValue{kind: jsonBool}},
			{"null", &jsonValue{kind: jsonNull}},
		} {
			if r.has(lit.word) {
				r.pos += len(lit.word)
				lit.v.off = start
				return lit.v, nil
			}
		}
		return nil, r.fail(start, "Parse error.")
	}
}

func (r *docReader) has(word string) bool {
	end := r.pos + len(word)
	if end > len(r.src) || string(r.src[r.pos:end]) != word {
		return false
	}
	return end == len(r.src) || !isIdentPart(rune(r.src[end]))
}

func (r *docReader) object(depth int) (*jsonValue, error) {
	v := &jsonValue{kind: jsonObject, off: r.pos}
	r.pos++ // {
	for {
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.pos >= len(r.src) {
			return nil, r.fail(r.pos, "Unexpected EOF.")
		}
		if r.src[r.pos] == '}' {
			v.end = r.pos
			r.pos++
			return v, nil
		}
		if r.src[r.pos] != '"' {
			return nil, r.fail(r.pos, `Expected '"'.`)
		}
		keyOff := r.pos
		key, _, err := r.str()
		if err != nil {
			return nil, err
		}
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.pos >= len(r.src) || r.src[r.pos] != ':' {
			return nil, r.fail(r.pos, "Expected ':'.")
		}
		r.pos++
		if err := r.space(); err != nil {
			return nil, err
		}
		val, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		v.members = append(v.members, jsonMember{key: key, keyOff: keyOff, val: val})
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.pos >= len(r.src) {
			return nil, r.fail(r.pos, "Unexpected EOF.")
		}
		switch r.src[r.pos] {
		case ',':
			r.pos++ // a trailing comma before } is accepted
		case '}':
		default:
			return nil, r.fail(r.pos, "Expected ',' or '}'.")
		}
	}
}

func (r *docReader) array(depth int) (*jsonValue, error) {
	v := &jsonValue{kind: jsonArray, off: r.pos}
	r.pos++ // [
	for {
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.pos >= len(r.src) {
			return nil, r.fail(r.pos, "Unexpected EOF.")
		}
		if r.src[r.pos] == ']' {
			r.pos++
			return v, nil
		}
		el, err := r.value(depth + 1)
		if err != nil {
			return nil, err
		}
		v.elems = append(v.elems, el)
		if err := r.space(); err != nil {
			return nil, err
		}
		if r.pos >= len(r.src) {
			return nil, r.fail(r.pos, "Unexpected EOF.")
		}
		switch r.src[r.pos] {
		case ',':
			r.pos++
		case ']':
		default:
			return nil, r.fail(r.pos, "Expected ',' or ']'.")
		}
	}
}

func (r *docReader) number() (*jsonValue, error) {
	start := r.pos
	if r.src[r.pos] == '-' {
		r.pos++
	}
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		if c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			r.pos++
			continue
		}
		break
	}
	if _, err := strconv.ParseFloat(string(r.src[start:r.pos]), 64); err != nil {
		return nil, r.fail(start, "Invalid number.")
	}
	return &jsonValue{kind: jsonNumber, off: start}, nil
}

// str reads a string literal and returns its decoded text with the document offset of each byte.
func (r *docReader) str() (string, []int, error) {
	start := r.pos
	r.pos++ // "
	var out []byte
	var offs []int
	emit := func(s string, off int) {
		for i := 0; i < len(s); i++ {
			out = append(out, s[i])
			offs = append(offs, off)
		}
	}
	for {
		if r.pos >= len(r.src) {
			return "", nil, r.fail(start, "Unterminated string.")
		}
		c := r.src[r.pos]
		switch {
		case c == '"':
			offs = append(offs, r.pos)
			r.pos++
			return string(out), offs, nil
		case c < 0x20:
			return "", nil, r.fail(r.pos, "Control character in string.")
		case c == '\\':
			at := r.pos
			if r.pos+1 >= len(r.src) {
				return "", nil, r.fail(at, "Unterminated string.")
			}
			e := r.src[r.pos+1]
			r.pos += 2
			switch e {
			case '"', '\\', '/':
				emit(string(e), at)
			case 'b':
				emit("\b", at)
			case 'f':
				emit("\f", at)
			case 'n':
				emit("\n", at)
			case 'r':
				emit("\r", at)
			case 't':
				emit("\t", at)
			case 'u':
				cp, ok := r.hex4()
				if !ok {
					return "", nil, r.fail(at, "Invalid escape in string.")
				}
				if utf16.IsSurrogate(cp) && r.pos+6 <= len(r.src) && r.src[r.pos] == '\\' && r.src[r.pos+1] == 'u' {
					save := r.pos
					r.pos += 2
					if lo, ok := r.hex4(); ok && utf16.DecodeRune(cp, lo) != utf8.RuneError {
						cp = utf16.DecodeRune(cp, lo)
					} else {
						r.pos = save
					}
				}
				emit(string(cp), at)
			default:
				return "", nil, r.fail(at, "Invalid escape in string.")
			}
		default:
			_, size := utf8.DecodeRune(r.src[r.pos:])
			emit(string(r.src[r.pos:r.pos+size]), r.pos)
			r.pos += size
		}
	}
}

func (r *docReader) hex4() (rune, bool) {
	if r.pos+4 > len(r.src) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(r.src[r.pos:r.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	r.pos += 4
	return rune(n), true
}

// position turns a byte offset into a 1-based line and column, counting columns in characters,
// as Firebase's "1:26:" prefixes do.
func position(src []byte, off int) (line, col int) {
	if off > len(src) {
		off = len(src)
	}
	line, col = 1, 1
	for i := 0; i < off; {
		c, size := utf8.DecodeRune(src[i:])
		if c == '\n' {
			line++
			col = 1
		} else {
			col++
		}
		i += size
	}
	return line, col
}

// StrictJSON returns a rules document as plain JSON, comments and trailing commas dropped and
// every value as written: what GET /.settings/rules.json?format=strict answers, and what the
// Admin SDKs' getRulesJSON parses (firebase-admin-node database.ts getRulesJSON).
func StrictJSON(src []byte) ([]byte, error) {
	v, err := readDocument(src)
	if err != nil {
		return nil, err
	}
	return v.appendJSON(nil, src), nil
}

func (v *jsonValue) appendJSON(b []byte, src []byte) []byte {
	switch v.kind {
	case jsonObject:
		b = append(b, '{')
		for i, m := range v.members {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, m.key)
			b = append(b, ':')
			b = m.val.appendJSON(b, src)
		}
		return append(b, '}')
	case jsonArray:
		b = append(b, '[')
		for i, el := range v.elems {
			if i > 0 {
				b = append(b, ',')
			}
			b = el.appendJSON(b, src)
		}
		return append(b, ']')
	case jsonString:
		return appendJSONString(b, v.str)
	case jsonNumber:
		end := v.off
		for end < len(src) && strings.IndexByte("0123456789.eE+-", src[end]) >= 0 {
			end++
		}
		return append(b, src[v.off:end]...)
	case jsonBool:
		return strconv.AppendBool(b, v.boolean)
	}
	return append(b, "null"...)
}

// appendJSONString appends s as a JSON string.
func appendJSONString(b []byte, s string) []byte {
	q, _ := json.Marshal(s)
	return append(b, q...)
}
