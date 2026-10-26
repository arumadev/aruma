package value

import (
	"errors"
	"strings"
)

// FieldPath is a path into a document's fields, one map key per segment. The document's name
// is the one-segment path __name__ (DocumentID).
type FieldPath []string

// DocumentID is the field path that names the document itself in queries and masks.
const DocumentID = "__name__"

// The limits production holds a field path in a mask, an update mask or a query to
// (values-mask.json).
const (
	// MaxPathSegments: "paths must have at most 20 elements" for 21.
	MaxPathSegments = 20
	// MaxPathSize bounds the sum of each segment's length plus 1: a×1499 passes and a×1500
	// fails with "property path is longer than 1500 bytes.", as do a.b×1497 and a.b×1498.
	MaxPathSize = 1500
)

// A field path's error messages, production's bytes. The regexes quoted are its own.
var (
	errEmptyPath    = errors.New("Invalid empty property path string.")
	errPathSegments = errors.New("paths must have at most 20 elements")
	errPathSize     = errors.New("property path is longer than 1500 bytes.")
)

func invalidPath(s string) error {
	return errors.New(`Invalid property path "` + s + `". Unquoted property paths must match regex ([a-zA-Z_][a-zA-Z_0-9]*), and quoted property paths must match regex (` +
		"`(?:[^`\\\\]|(?:\\\\.))+`)")
}

// ParseFieldPath parses a field path in Firestore's syntax: segments separated by dots, each
// either simple ([a-zA-Z_][a-zA-Z_0-9]*) or quoted in backticks, where a backslash escapes the
// next character (`a.b`, `\“, `é`). A non-simple segment must be quoted: "é", "a b" and "1n"
// are refused, "`é`" is the key é. It enforces MaxPathSegments and MaxPathSize, with
// production's messages.
func ParseFieldPath(s string) (FieldPath, error) {
	if s == "" {
		return nil, errEmptyPath
	}
	var p FieldPath
	rest := s
	for {
		var seg string
		var ok bool
		if strings.HasPrefix(rest, "`") {
			seg, rest, ok = quotedSegment(rest[1:])
		} else {
			seg, rest, ok = simpleSegment(rest)
		}
		if !ok {
			return nil, invalidPath(s)
		}
		p = append(p, seg)
		if rest == "" {
			break
		}
		if rest[0] != '.' || len(rest) == 1 {
			return nil, invalidPath(s)
		}
		rest = rest[1:]
	}
	if p.size() > MaxPathSize {
		return nil, errPathSize
	}
	if len(p) > MaxPathSegments {
		return nil, errPathSegments
	}
	return p, nil
}

// simpleSegment reads [a-zA-Z_][a-zA-Z_0-9]* at the start of s.
func simpleSegment(s string) (seg, rest string, ok bool) {
	i := 0
	for i < len(s) && (isIdentStart(s[i]) || i > 0 && s[i] >= '0' && s[i] <= '9') {
		i++
	}
	return s[:i], s[i:], i > 0
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// quotedSegment reads a quoted segment's body and its closing backtick; the body may not be
// empty.
func quotedSegment(s string) (seg, rest string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 == len(s) {
				return "", "", false
			}
			i++
			b.WriteByte(s[i])
		case '`':
			if b.Len() == 0 {
				return "", "", false
			}
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", "", false
}

// size is the path's length as production bounds it: each segment's bytes plus 1.
func (p FieldPath) size() int {
	n := 0
	for _, seg := range p {
		n += len(seg) + 1
	}
	return n
}

// String formats the path in the syntax ParseFieldPath reads: simple segments as they are,
// others in backticks with ` and \ escaped.
func (p FieldPath) String() string {
	var b strings.Builder
	for i, seg := range p {
		if i > 0 {
			b.WriteByte('.')
		}
		if isSimple(seg) {
			b.WriteString(seg)
			continue
		}
		b.WriteByte('`')
		for j := 0; j < len(seg); j++ {
			if seg[j] == '`' || seg[j] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(seg[j])
		}
		b.WriteByte('`')
	}
	return b.String()
}

func isSimple(seg string) bool {
	s, rest, ok := simpleSegment(seg)
	return ok && rest == "" && s == seg
}

// IsDocumentID reports whether p is __name__, the document's own name.
func (p FieldPath) IsDocumentID() bool { return len(p) == 1 && p[0] == DocumentID }

// IsPrefixOf reports whether p is q or one of its ancestors.
func (p FieldPath) IsPrefixOf(q FieldPath) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// Lookup returns the value at path p in v, a map value: each segment but the last must name a
// map (an array is not entered).
func (v Value) Lookup(p FieldPath) (Value, bool) {
	for _, seg := range p {
		f, ok := v.Get(seg)
		if !ok {
			return Value{}, false
		}
		v = f
	}
	return v, len(p) > 0
}
