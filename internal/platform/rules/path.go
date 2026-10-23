package rules

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// pathPart is one part of a path literal, as the interpreter has evaluated it.
type pathPart struct {
	text  string // literal text as written, still URL-encoded
	value Value  // the value of a $(…)
	isVal bool   // the part is a $(…)
}

// buildPath builds the Path value of a path literal: literal text is URL-decoded as Java's
// URLDecoder does, a $(x) string is one segment, and a $(p) path inserts all of its segments
// (ADR-021 §5.7; hidane interp.rs). Any other value of a $(…) is one segment as string() writes
// it (an int, a float, a bool, null), and an error where string() is one.
func buildPath(m *meter, parts []pathPart) (Value, error) {
	n, size := 0, 0
	for _, p := range parts {
		if !p.isVal {
			n, size = n+1, size+len(p.text)
			continue
		}
		if err := reserved(p.value); err != nil {
			return Value{}, err
		}
		if p.value.kind == KindPath {
			for _, s := range p.value.segments() {
				n, size = n+1, size+len(s)
			}
		} else {
			n++
		}
	}
	if err := chargeList(m, n); err != nil {
		return Value{}, err
	}
	if err := m.alloc(int64(size)); err != nil {
		return Value{}, err
	}
	segs := make([]string, 0, n)
	for _, p := range parts {
		switch {
		case !p.isVal:
			seg, err := urlDecode(p.text)
			if err != nil {
				return Value{}, err
			}
			segs = append(segs, seg)
		case p.value.kind == KindPath:
			segs = append(segs, p.value.segments()...)
		default:
			s, err := toString(p.value)
			if err != nil {
				return Value{}, err
			}
			if err := chargeString(m, len(s)); err != nil {
				return Value{}, err
			}
			segs = append(segs, s)
		}
	}
	return pathOf(segs), nil
}

// urlDecode decodes a segment as Java's URLDecoder does: + is a space, and each run of %XX
// escapes is bytes decoded as UTF-8, a byte that is not UTF-8 becoming U+FFFD by itself
// (path('%FF%FF')[0].size() == 2). An escape that is not two hex digits, or that the segment
// ends before, is an error with production's text (the review's probes of the Rules Test API,
// 2026-10-24: path('a%zz'), path('a%'), path('a%2')). hidane, the emulator, keeps them as they
// are; production wins. The result is never longer than s.
func urlDecode(s string) (string, error) {
	if !strings.ContainsAny(s, "%+") {
		return s, nil
	}
	fail := func(why string) error {
		return errorf("Unable to decode %s to url. Error: URLDecoder: %s", s, why)
	}
	out := make([]byte, 0, len(s))
	var run []byte
	for i := 0; i < len(s); {
		switch s[i] {
		case '+':
			out = append(out, ' ')
			i++
		case '%':
			run = run[:0]
			for i < len(s) && s[i] == '%' {
				// Two characters must follow, then both must be hex digits; the first that is not
				// is named.
				var chars [2]rune
				at := i + 1
				for k := range 2 {
					if at >= len(s) {
						return "", fail("Incomplete trailing escape (%) pattern")
					}
					r, n := decodeWTF8(s[at:])
					chars[k] = r
					at += n
				}
				var digits [2]byte
				for k, r := range chars {
					if r >= utf8.RuneSelf || !isHex(byte(r)) {
						return "", fail(fmt.Sprintf("Illegal hex characters in escape (%%) pattern - not a hexadecimal digit: \"%c\" = %d", r, r))
					}
					digits[k] = byte(r)
				}
				run = append(run, unhex(digits[0])<<4|unhex(digits[1]))
				i = at
			}
			for j := 0; j < len(run); {
				r, n := utf8.DecodeRune(run[j:])
				out = utf8.AppendRune(out, r) // U+FFFD for each byte that is not UTF-8
				j += n
			}
		default:
			out = append(out, s[i])
			i++
		}
	}
	return string(out), nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// parsePath is path(s): with or without a leading slash, no empty segment, each segment
// URL-decoded as a path literal's (hidane library.rs). A segment may be a template variable,
// {name} or {name=**}, which bind() fills (Pyric path-constructor-and-bind, production:
// path('users/{uid}').bind({'uid': 'alice'}) == path('users/alice')).
func parsePath(m *meter, s string) (Value, error) {
	if s == "" {
		return Value{}, errorf("Empty Path Template is not allowed.")
	}
	body := strings.TrimPrefix(s, "/")
	if err := chargeList(m, strings.Count(body, "/")+1); err != nil {
		return Value{}, err
	}
	if err := m.alloc(int64(len(body))); err != nil {
		return Value{}, err
	}
	parts := strings.Split(body, "/")
	for _, p := range parts {
		if p == "" {
			return Value{}, errorf("Invalid Path Template %s. Empty segment is not allowed.", s)
		}
	}
	for i, p := range parts {
		seg, err := urlDecode(p)
		if err != nil {
			return Value{}, err
		}
		parts[i] = seg
	}
	return pathOf(parts), nil
}

// pathString is string(p): each segment after a slash, and "/" for no segment.
func pathString(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/")
}

// templateVar returns the name of a template segment, {name} or {name=**}.
func templateVar(seg string) (name string, rest, ok bool) {
	if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' {
		return "", false, false
	}
	name = seg[1 : len(seg)-1]
	if n, isRest := strings.CutSuffix(name, "=**"); isRest {
		return n, true, n != ""
	}
	return name, false, name != "" && !strings.ContainsAny(name, "{}=")
}

// pathMethod calls p.name(args): bind(map), which fills the path's template variables with the
// map's values: a string is one segment, a path all of its segments, anything else one segment
// as string() writes it. A key that names no variable is an error (hidane's message); an empty
// map leaves the path as it is.
func pathMethod(m *meter, segs []string, name string, args []Value) (Value, error) {
	if name != "bind" {
		return Value{}, errNotFound(name)
	}
	if len(args) != 1 || args[0].kind != KindMap {
		return Value{}, mismatch("path.bind", args, []string{"map"})
	}
	bindings := args[0].mapv()
	if len(bindings.keys) == 0 {
		return pathOf(segs), nil
	}
	if err := m.work(int64(len(segs) + len(bindings.keys))); err != nil {
		return Value{}, err
	}
	// What the result may hold: every segment, and every segment of a path bound into one.
	upper, size := len(segs), 0
	for _, v := range bindings.entries {
		if v.kind == KindPath {
			upper += len(v.segments())
			for _, s := range v.segments() {
				size += len(s)
			}
		}
	}
	if err := chargeList(m, upper); err != nil {
		return Value{}, err
	}
	if err := m.alloc(int64(size)); err != nil {
		return Value{}, err
	}
	used := map[string]bool{}
	out := make([]string, 0, upper)
	for _, seg := range segs {
		v, _, isVar := templateVar(seg)
		value, bound := bindings.entries[v]
		if !isVar || !bound {
			out = append(out, seg)
			continue
		}
		used[v] = true
		if err := reserved(value); err != nil {
			return Value{}, err
		}
		if value.kind == KindPath {
			out = append(out, value.segments()...)
			continue
		}
		s, err := toString(value)
		if err != nil {
			return Value{}, err
		}
		if err := chargeString(m, len(s)); err != nil {
			return Value{}, err
		}
		out = append(out, s)
	}
	var missing []string
	for _, k := range bindings.keys {
		if !used[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Value{}, errorf("Variable(s) %s do not exist in the path template.", strings.Join(missing, ", "))
	}
	return pathOf(out), nil
}
