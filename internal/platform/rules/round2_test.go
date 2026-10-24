package rules

import (
	"errors"
	"strings"
	"testing"
)

// The review of S1-B, round 2 (2026-10-24): the matcher's charge, and lone surrogates in the
// matcher.

// doubled returns base doubled n times, as a rule builds it (function d(s) { return s + s; }),
// on m's account.
func doubled(t *testing.T, m *meter, base string, n int) Value {
	t.Helper()
	s := String(base)
	for range n {
		var err error
		if s, err = binary(m, tokPlus, s, s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// What production splits and replaces in one evaluation, the strings built by doubling
// (production, the review's round 2 probes of the Rules Test API, 2026-10-24): the matcher's
// charge must not refuse them.
func TestSplitCostAsProduction(t *testing.T) {
	m := newTestMeter()
	mib := doubled(t, m, strings.Repeat("x", 4095)+",", 8) // 1 MiB, 256 commas
	parts, err := callMethod(m, mib, "split", []Value{String(",")})
	if err != nil || len(parts.items()) != 256 {
		t.Errorf("1 MiB split(',') = %d parts, %v; production allows 256", len(parts.items()), err)
	}

	m = newTestMeter()
	kib := doubled(t, m, ","+strings.Repeat("x", 127), 10) // 128 KiB, 1024 commas
	parts, err = callMethod(m, kib, "split", []Value{String(",")})
	if err != nil || len(parts.items()) != 1025 {
		t.Errorf("128 KiB split(',') = %d parts, %v; production allows 1025", len(parts.items()), err)
	}

	m = newTestMeter()
	mib = doubled(t, m, strings.Repeat("x", 4095)+",", 8)
	out, err := callMethod(m, mib, "replace", []Value{String(","), String(";")})
	if err != nil || len(out.str()) != 1<<20 || strings.Contains(out.str(), ",") {
		t.Errorf("1 MiB replace(',', ';') = %d bytes, %v; production allows it", len(out.str()), err)
	}

	// The same through the windowed search: a class is no literal, but its width is bounded.
	m = newTestMeter()
	mib = doubled(t, m, strings.Repeat("x", 4095)+",", 8)
	if parts, err := callMethod(m, mib, "split", []Value{String("[,;]")}); err != nil || len(parts.items()) != 256 {
		t.Errorf("1 MiB split('[,;]') = %d parts, %v", len(parts.items()), err)
	}

	// Production refuses a 1 MiB split into 524,288 parts: "Unable to evaluate the expression as
	// the maximum memory usage of 1000 has been reached." Here the allocation limit refuses it,
	// with Aruma's own text (docs/compat/rules-library-provisional.md).
	m = newTestMeter()
	many := doubled(t, m, "a,", 19)
	_, err = callMethod(m, many, "split", []Value{String(",")})
	var a *abort
	if !errors.As(err, &a) {
		t.Errorf("1 MiB split into 524,288 parts = %v, want an abort", err)
	}
	t.Logf("1 MiB split into 524,288 parts: %v", err)
}

// allMatches returns every match the matcher finds of p in s, searched with the given width.
func allMatches(t *testing.T, p *compiledRegex, s string, width int) []found {
	t.Helper()
	q := &compiledRegex{text: p.text, insts: p.insts, groups: p.groups, regexForms: p.regexForms, width: width}
	it := &matcher{m: newTestMeter(), p: q, forms: &q.regexForms, plain: true, s: s, in: s}
	var got []found
	for {
		f, ok, err := it.next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return got
		}
		got = append(got, f)
	}
}

// The windowed search finds what a search to the end of the string finds, at the windows'
// edges too.
func TestWindowedSearch(t *testing.T) {
	long := strings.Repeat("ab", 700) + "x" + strings.Repeat("ab", 300)
	for _, c := range []struct{ s, pattern string }{
		{long, "x"}, {long, "bx?a"}, {long, "b$"}, {long, `\bab`}, {long, "(a)(b)"}, {long, "a|bx"},
		{long, ".{3}x"}, {long, "x?"}, {long, `b\b`}, {long, "(?m)^a"},
		{strings.Repeat("😀a", 400), "a|😀"}, {strings.Repeat("é", 1000) + "z", "z$|é{2}"},
	} {
		m := newTestMeter()
		p, err := m.regexes.get(m, c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if p.width < 0 {
			t.Fatalf("%q has no bound", c.pattern)
		}
		windowed, whole := allMatches(t, p, c.s, p.width), allMatches(t, p, c.s, -1)
		if len(windowed) != len(whole) {
			t.Errorf("%q: %d matches windowed, %d to the end", c.pattern, len(windowed), len(whole))
			continue
		}
		for i := range whole {
			if windowed[i].start != whole[i].start || windowed[i].end != whole[i].end {
				t.Errorf("%q: match %d windowed %+v, to the end %+v", c.pattern, i, windowed[i], whole[i])
				break
			}
		}
	}
	for pattern, want := range map[string]int{"abc": 12, "a|bcd": 12, "a?b{2,3}": 16, "a*": -1, "(a|b)+": -1, `\bx$`: 4, "": 0} {
		re := mustParse(t, pattern)
		if got := matchWidth(re); got != want {
			t.Errorf("matchWidth(%q) = %d, want %d", pattern, got, want)
		}
	}
}

// The matcher reads a lone surrogate as its own code point (production, the review's round 2
// probes of the Rules Test API, 2026-10-24; half is '😀'[0]).
func TestLoneSurrogatesInPatterns(t *testing.T) {
	half := vs(lone(0xD83D))
	runValueCases(t, []valueCase{
		{"half.matches('\\uFFFD') is false", vCall(half, "matches", vs("�")), vb(false), ""},
		{"half.matches('[\\x{D800}-\\x{DFFF}]')", vCall(half, "matches", vs(`[\x{D800}-\x{DFFF}]`)), vb(true), ""},
		{"half.matches('[^\\uFFFD]')", vCall(half, "matches", vs("[^�]")), vb(true), ""},
		{"half.matches('.')", vCall(half, "matches", vs(".")), vb(true), ""},
		{"('a' + half + 'b').split('\\uFFFD').size() is 1", vThen(vCall(vs("a"+lone(0xD83D)+"b"), "split", vs("�")), "size"), vi(1), ""},
		{"half.matches('\\x{D83D}')", vCall(half, "matches", vs(`\x{D83D}`)), vb(true), ""},
		{"half.matches('\\x{E03D}'), its sentinel, is false", vCall(half, "matches", vs(`\x{E03D}`)), vb(false), ""},
		{"half.matches('[\\x{E000}-\\x{E7FF}]') is false", vCall(half, "matches", vs(`[\x{E000}-\x{E7FF}]`)), vb(false), ""},
		{"a split between halves reads them", vThen(vCall(vs("a😀b"), "split", vs(`[\x{DC00}-\x{DFFF}]`)), "size"), vi(1), ""},
		{"a split on the empty pattern, then the low half", vOpOn(vThen(vCall(vs("😀"), "split", vs("")), "join", vs("|")), tokEQ, vLit(vs("|"+lone(0xD83D)+"|"+lone(0xDE00)))), vb(true), ""},
		// A private-use character of the first block beside a lone surrogate: the next block.
		{"U+E000 beside a half", vCall(vs(lone(0xD83D)+"\uE000"), "matches", vs(`[\x{D800}-\x{DFFF}]\x{E000}`)), vb(true), ""},
		{"U+E000 is no surrogate beside a half", vCall(vs(lone(0xD83D)+"\uE000"), "matches", vs(`[\x{D800}-\x{DFFF}]{2}`)), vb(false), ""},
		// Without a lone surrogate, the sentinels' range is itself.
		{"U+E000 alone", vCall(vs(""), "matches", vs(`[\x{E000}-\x{E7FF}]`)), vb(true), ""},
		// toUtf8 and hashing of a half read it as '?' (production, round 2: b'?', size 1).
		{"half.toUtf8() == b'?'", vCall(half, "toUtf8"), vbytes("?"), ""},
		{"md5(half) == md5('?')", vOpOn(vFn("hashing.md5", half), tokEQ, vFn("hashing.md5", vs("?"))), vb(true), ""},
	})
}

// A replacement of many pieces allocates what it is charged (the review's round 2: x$1 × 2^17
// allocated 33.9 MiB against 12 MiB charged).
func TestReplacementIsCharged(t *testing.T) {
	m := newTestMeter()
	repl := strings.Repeat("x$1", 1<<17)
	var err error
	mem := allocated(func() { _, err = replaceString(m, "a", "(a)", repl) })
	if err != nil {
		t.Fatal(err)
	}
	if !raceEnabled && mem > uint64(m.allocated)*3/2 {
		t.Errorf("replace allocated %d KiB, charged %d KiB", mem>>10, m.allocated>>10)
	}
}
