package rules

import (
	"strings"
	"testing"
)

// Strings are WTF-8 (wtf8.go): the halves of a pair are lone surrogates, and joining them makes
// the pair again. Production's answers are the review's probes (probes_data_test.go).
func TestWTF8(t *testing.T) {
	hi, lo := lone(0xD83D), lone(0xDE00)
	if hi != "\xED\xA0\xBD" || lo != "\xED\xB8\x80" {
		t.Fatalf("lone surrogates = % x, % x", hi, lo)
	}
	if got := joinWTF8(hi, lo); got != "😀" {
		t.Errorf("hi + lo = % x, want the pair's four bytes", got)
	}
	if got := joinWTF8(lo, hi); got != lo+hi {
		t.Errorf("lo + hi joined: % x", got)
	}
	if !joins("a"+hi, lo+"b") || joins("a", lo) || joins(hi, "x") {
		t.Error("joins")
	}
	for s, want := range map[string]int{"": 0, "abc": 3, "é": 1, "😀": 2, "a😀b": 4, hi: 1, hi + "x": 2, "نص 🙂": 5} {
		if got := utf16Len(s); got != want {
			t.Errorf("utf16Len(%q) = %d, want %d", s, got, want)
		}
	}
	s := "a😀b"
	for _, c := range []struct {
		from, to int
		want     string
	}{
		{0, 1, "a"}, {1, 3, "😀"}, {1, 2, hi}, {2, 3, lo}, {2, 4, lo + "b"}, {0, 2, "a" + hi}, {2, 2, ""}, {0, 4, s},
	} {
		a, b, ok := unitOffsets(s, c.from, c.to)
		if got := sliceAt(s, a, b); !ok || got != c.want {
			t.Errorf("%q[%d:%d] = % x, %v; want % x", s, c.from, c.to, got, ok, c.want)
		}
	}
	if _, _, ok := unitOffsets(s, 0, 5); ok {
		t.Error("unit 5 of a 4-unit string found")
	}
	// Order by unit: a lone high surrogate before 'x' against the pair it starts: the second units
	// are 'x' and the low surrogate.
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"😀", "￿", -1}, {hi, "😀", -1}, {hi + "￿", "😀", 1}, {hi + "x", "😀", -1},
		{"abc", "abd", -1}, {"ab", "abc", -1}, {"é", "é", 0}, {lo, hi, 1},
	} {
		if got := compareUTF16(c.a, c.b); got != c.want {
			t.Errorf("compare(% x, % x) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := toUTF8("a" + hi + "b"); got != "a?b" {
		t.Errorf("toUTF8 = %q, want a?b", got)
	}
	if got := loneView("a"+hi, 0xE000); got != "a\ue03d" || len(got) != len("a"+hi) {
		t.Errorf("loneView = % x", got)
	}
	if r, n := decodeWTF8(lo + "x"); r != 0xDE00 || n != 3 {
		t.Errorf("decodeWTF8 = %x, %d", r, n)
	}
	runValueCases(t, []valueCase{
		{"'😀'[0] + '😀'[1] == '😀'", vOpOn(vLit(vs(hi)), tokPlus, vLit(vs(lo))), vs("😀"), ""},
		{"('😀'[0] + 'x').size()", vThen(vOp(vs(hi), tokPlus, vs("x")), "size"), vi(2), ""},
		{"join joins halves", vCall(vl(vs(hi), vs(lo)), "join", vs("")), vs("😀"), ""},
		{"a lone surrogate hashes as '?'", vOpOn(vFn("hashing.md5", vs(hi)), tokEQ, vFn("hashing.md5", vs("?"))), vb(true), ""},
		{"replace('', '') joins the halves back", vCall(vs("😀"), "replace", vs(""), vs("")), vs("😀"), ""},
		{"split('') cuts the pair", vCall(vs("😀"), "split", vs("")), vl(vs(""), vs(hi), vs(lo)), ""},
		{"a lone half matches .", vCall(vs(hi), "matches", vs(".")), vb(true), ""},
	})
	if strings.Contains(hi, "�") {
		t.Error("a lone surrogate is U+FFFD")
	}
}
