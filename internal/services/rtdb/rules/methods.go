package rules

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// maxKeyBytes is the longest key Firebase stores; a longer one in child() cannot exist.
const maxKeyBytes = 768

func (ev *evaluator) node(s snap) Node {
	if s.voidAt >= 0 {
		return Node{}
	}
	if s.fresh {
		return ev.w.fresh(s.path)
	}
	return ev.w.old(s.path)
}

// child resolves a relative path the way Firebase does: slashes at either end and doubled
// slashes are ignored, child("") is the snapshot itself, and a key that cannot exist, such as
// "a.b", gives an empty snapshot rather than an error (emulator 4.11.2).
func (s snap) child(rel string) snap {
	out := snap{fresh: s.fresh, path: s.path[:len(s.path):len(s.path)], voidAt: s.voidAt}
	for _, k := range strings.Split(rel, "/") {
		if k == "" {
			continue
		}
		if out.voidAt < 0 && (!validKey(k) || len(k) > maxKeyBytes) {
			out.voidAt = len(out.path)
		}
		out.path = append(out.path, k)
	}
	return out
}

func (ev *evaluator) snapshotMethod(m method, s snap, args []any) (any, error) {
	switch m {
	case mVal:
		n := ev.node(s)
		if n.HasChildren {
			return branch{}, nil
		}
		return n.Value, nil
	case mChild:
		rel, ok := args[0].(string)
		if !ok {
			return nil, fail("child() needs a string")
		}
		return s.child(rel), nil
	case mParent:
		if len(s.path) == 0 {
			return nil, fail("the root has no parent")
		}
		p := snap{fresh: s.fresh, path: s.path[:len(s.path)-1], voidAt: s.voidAt}
		if p.voidAt >= len(p.path) {
			p.voidAt = -1
		}
		return p, nil
	case mHasChild:
		rel, ok := args[0].(string)
		if !ok {
			return nil, fail("hasChild() needs a string")
		}
		return ev.node(s.child(rel)).exists(), nil
	case mHasChildren:
		if len(args) == 0 {
			return ev.node(s).HasChildren, nil
		}
		for _, k := range args[0].([]any) {
			rel, ok := k.(string)
			if !ok {
				return nil, fail("hasChildren() needs strings")
			}
			if !ev.node(s.child(rel)).exists() {
				return false, nil
			}
		}
		return true, nil
	case mExists:
		return ev.node(s).exists(), nil
	case mIsNumber:
		_, ok := ev.node(s).Value.(float64)
		return ok, nil
	case mIsString:
		_, ok := ev.node(s).Value.(string)
		return ok, nil
	case mIsBoolean:
		_, ok := ev.node(s).Value.(bool)
		return ok, nil
	}
	return nil, fail("unknown snapshot method")
}

// Strings in rules behave as Java strings: length counts UTF-16 code units ('😀'.length is 2),
// comparison is by code unit, and case mapping is Java's, which is Unicode's full mapping
// ('straße'.toUpperCase() is 'STRASSE', 'ΣΑΣ'.toLowerCase() is 'σας'). Emulator 4.11.2.

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func compareUTF16(a, b string) int {
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			ua, ub := utf16Units(ra), utf16Units(rb)
			for i := 0; i < len(ua) && i < len(ub); i++ {
				if ua[i] != ub[i] {
					if ua[i] < ub[i] {
						return -1
					}
					return 1
				}
			}
			if len(ua) < len(ub) {
				return -1
			}
			return 1
		}
		a, b = a[na:], b[nb:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func utf16Units(r rune) []uint16 {
	if r1, r2 := utf16.EncodeRune(r); r1 != unicode.ReplacementChar {
		return []uint16{uint16(r1), uint16(r2)}
	}
	return []uint16{uint16(r)}
}

func containsString(s, sub string) bool { return strings.Contains(s, sub) }

// replaceAll replaces every occurrence: replace() in rules is not JavaScript's (reference).
func replaceAll(s, old, repl string) string { return strings.ReplaceAll(s, old, repl) }

// upperSpecial lists Unicode's unconditional one-to-many uppercase mappings (SpecialCasing.txt)
// that Go's simple mapping lacks, for the characters text in rules plausibly holds.
var upperSpecial = map[rune]string{
	'ß': "SS", 'ŉ': "ʼN", 'ǰ': "J̌", 'ΐ': "Ϊ́", 'ΰ': "Ϋ́", 'և': "ԵՒ",
	'ẖ': "H̱", 'ẗ': "T̈", 'ẘ': "W̊", 'ẙ': "Y̊", 'ẚ': "Aʾ",
	'ﬀ': "FF", 'ﬁ': "FI", 'ﬂ': "FL", 'ﬃ': "FFI", 'ﬄ': "FFL", 'ﬅ': "ST", 'ﬆ': "ST",
	'ﬓ': "ՄՆ", 'ﬔ': "ՄԵ", 'ﬕ': "ՄԻ", 'ﬖ': "ՎՆ", 'ﬗ': "ՄԽ",
}

func javaUpper(s string) string {
	var b strings.Builder
	for _, r := range s {
		if u, ok := upperSpecial[r]; ok {
			b.WriteString(u)
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// javaLower lowercases with Java's two special cases: İ becomes i followed by a combining dot,
// and Σ becomes ς at the end of a word.
func javaLower(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch r {
		case 'İ':
			b.WriteString("i" + string(rune(0x307))) // i and a combining dot above
			continue
		case 'Σ':
			if finalSigma(runes, i) {
				b.WriteRune('ς')
				continue
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// finalSigma reports whether the Σ at i ends a word: a cased letter before it and none after.
func finalSigma(rs []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if unicode.Is(unicode.Mn, rs[j]) {
			continue
		}
		before = unicode.IsUpper(rs[j]) || unicode.IsLower(rs[j]) || unicode.IsTitle(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if unicode.Is(unicode.Mn, rs[j]) {
			continue
		}
		return !(unicode.IsUpper(rs[j]) || unicode.IsLower(rs[j]) || unicode.IsTitle(rs[j]))
	}
	return true
}

// javaNumber turns a number into text as rules do when it meets a string: a whole number that
// fits in a 64-bit integer prints as one ("1", "9200000000000000000", "0" for -0), anything else
// as Java's Double.toString ("0.5", "1.0E21", "1.0E-4", "NaN", "Infinity"). Emulator 4.11.2.
func javaNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == math.Trunc(f) && f >= -9.223372036854775808e18 && f < 9.223372036854775808e18:
		return strconv.FormatInt(int64(f), 10)
	}
	abs := math.Abs(f)
	if abs >= 1e-3 && abs < 1e7 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	// Shortest digits in scientific form, then Java's spelling: 1.0E21, 1.5E-7.
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(s, "e")
	if !strings.Contains(mant, ".") {
		mant += ".0"
	}
	n, _ := strconv.Atoi(exp)
	return mant + "E" + strconv.Itoa(n)
}
