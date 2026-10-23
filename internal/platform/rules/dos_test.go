package rules

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The holes the review of S1-B found in the library's bounds (2026-10-24), each as the review
// reproduced it: an evaluation of as many operations as the expression limit allows must end,
// allowed or denied, in bounded time and memory, everything it built charged. A call is either
// fast or refused by the meter before it runs.

// bounded runs f, which makes up to expressionLimit operations on m and stops at the first
// abort, and checks it ended within limit, with no more allocated than the meter's caps allow.
func bounded(t *testing.T, name string, m *meter, limit time.Duration, f func() error) error {
	t.Helper()
	var err error
	start := time.Now()
	mem := allocated(func() { err = f() })
	d := time.Since(start)
	t.Logf("%s: %v, work %d, charged %d KiB, allocated %d KiB, %v", name, d, m.worked, m.allocated>>10, mem>>10, err)
	if d > within(limit) {
		t.Errorf("%s: took %v, past %v (work %d of %d)", name, d, within(limit), m.worked, m.lim.Work)
	}
	if !raceEnabled && mem > 4*uint64(m.lim.AllocBytes) {
		t.Errorf("%s: allocated %d MiB, charged %d MiB", name, mem>>20, m.allocated>>20)
	}
	if m.allocated > m.lim.AllocBytes {
		t.Errorf("%s: charged %d bytes, past the cap %d", name, m.allocated, m.lim.AllocBytes)
	}
	return err
}

// repeat runs op expressionLimit times, stopping at its first error.
func repeat(op func() error) error {
	for range expressionLimit {
		if err := op(); err != nil {
			return err
		}
	}
	return nil
}

// Index, slice, size and < on a long string walk it: each is charged by the string's length.
// The review: 1000 × s[0] on 2 MiB of é took 27.6 s and 6 GB, charged nothing.
func TestLongStringOpsAreBounded(t *testing.T) {
	s := String(strings.Repeat("é", 1<<20)) // 2 MiB, as a service may pass it
	u := String(strings.Repeat("é", 1<<20-1) + "z")
	lo, hi := Int(1), Int(1<<20-1)
	for _, c := range []struct {
		name string
		op   func(m *meter) error
	}{
		{"s[0]", func(m *meter) error { _, err := selectIndex(m, s, Int(0)); return err }},
		{"s[2^20-1]", func(m *meter) error { _, err := selectIndex(m, s, Int(1<<20-1)); return err }},
		{"s[1:2^20-1]", func(m *meter) error { _, err := selectSlice(m, s, &lo, &hi); return err }},
		{"s < u", func(m *meter) error { _, err := binary(m, tokLT, s, u); return err }},
		{"s.size()", func(m *meter) error { _, err := callMethod(m, s, "size", nil); return err }},
		{"s == u", func(m *meter) error { _, err := binary(m, tokEQ, s, u); return err }},
		{"s.toUtf8()", func(m *meter) error { _, err := callMethod(m, s, "toUtf8", nil); return err }},
		{"hashing.sha256(s)", func(m *meter) error { _, err := callFunction(m, "hashing.sha256", []Value{s}); return err }},
		{"s in {s: 1}", func(m *meter) error {
			_, err := binary(m, tokIn, s, Map(map[string]Value{s.str(): Int(1)}))
			return err
		}},
	} {
		m := newTestMeter()
		err := bounded(t, c.name, m, 3*time.Second, func() error { return repeat(func() error { return c.op(m) }) })
		var a *abort
		if err != nil && !errors.As(err, &a) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A list of references to one long string, probed with in, toSet and hasAny: every comparison
// and hash is charged by the string's length. The review: one t in l took 4.1 s for 1% of the
// work limit.
func TestLongStringsInCollectionsAreBounded(t *testing.T) {
	m := newTestMeter()
	s := String("x")
	for len(s.str()) < 1<<20 {
		var err error
		if s, err = binary(m, tokPlus, s, s); err != nil {
			t.Fatal(err)
		}
	}
	l := List([]Value{s})
	for len(l.items()) < 1<<17 {
		var err error
		if l, err = callMethod(m, l, "concat", []Value{l}); err != nil {
			t.Fatal(err)
		}
	}
	end := Int(1<<20 - 1)
	head, err := selectSlice(m, s, nil, &end)
	if err != nil {
		t.Fatal(err)
	}
	tv, err := binary(m, tokPlus, head, String("y"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		op   func(m *meter) error
	}{
		{"t in l", func(m *meter) error { _, err := binary(m, tokIn, tv, l); return err }},
		{"l.toSet()", func(m *meter) error { _, err := callMethod(m, l, "toSet", nil); return err }},
		{"l.hasAny([t])", func(m *meter) error { _, err := callMethod(m, l, "hasAny", []Value{List([]Value{tv})}); return err }},
		{"l == l2", func(m *meter) error {
			_, err := binary(m, tokEQ, l, List(append(append([]Value{}, l.items()[1:]...), tv)))
			return err
		}},
	} {
		m := newTestMeter()
		err := bounded(t, c.name, m, 3*time.Second, func() error { return repeat(func() error { return c.op(m) }) })
		var a *abort
		if !errors.As(err, &a) {
			t.Errorf("%s: %v, want an abort on the work limit", c.name, err)
		}
	}
}

// replace() charges the pieces of its replacement before it reads them, and each match's use of
// them before it applies them. The review: 3 × replace('(b)?a', '$1' × 2^19) over 2000 matches
// took 21.3 s and charged nothing.
func TestReplacePiecesAreBounded(t *testing.T) {
	m := newTestMeter()
	repl := strings.Repeat("$1", 1<<19)
	s := strings.Repeat("a", 2000)
	err := bounded(t, "replace with 2^19 pieces", m, 3*time.Second, func() error {
		return repeat(func() error { _, err := replaceString(m, s, "(b)?a", repl); return err })
	})
	var a *abort
	if !errors.As(err, &a) {
		t.Errorf("replace with 2^19 pieces = %v, want an abort", err)
	}
}

// Capture groups multiply what each step of a search costs: split and replace charge them. The
// review: split('(a?)' × 1900) over 4300 a's took 7.6 s within one evaluation's work.
func TestCaptureGroupsAreBounded(t *testing.T) {
	pattern := strings.Repeat("(a?)", 1900)
	s := strings.Repeat("a", 4300)
	for _, c := range []struct {
		name string
		op   func(m *meter) error
	}{
		{"split", func(m *meter) error { _, err := splitString(m, s, pattern); return err }},
		{"replace", func(m *meter) error { _, err := replaceString(m, s, pattern, "$1900"); return err }},
	} {
		m := newTestMeter()
		bounded(t, c.name, m, 3*time.Second, func() error { return repeat(func() error { return c.op(m) }) })
	}
}

// Patterns that match nothing, and patterns whose priorities look far ahead, make each search
// cost the rest of the string: every search is charged that, so an evaluation of them ends in
// bounded time.
func TestSearchesAreBounded(t *testing.T) {
	for _, c := range []struct{ name, s, pattern string }{
		{"split('') of 20,000 characters", strings.Repeat("a", 20_000), ""},
		{"split('') of 10,000 pairs", strings.Repeat("😀", 10_000), ""},
		{"split('x*') of 20,000", strings.Repeat("a", 20_000), "x*"},
		{"a lookahead to the end at every match", strings.Repeat("a", 50_000), "a(?:a*b)|a"},
	} {
		m := newTestMeter()
		bounded(t, c.name, m, 3*time.Second, func() error {
			return repeat(func() error { _, err := splitString(m, c.s, c.pattern); return err })
		})
	}
}

// (?i) folds a class range one character at a time: parseCost counts them, so a pattern of
// wide folded classes is refused before it is parsed, and one that fits is charged its four
// parses. The review: (?i) and 400 × [\x{100}-\x{10FFFF}] took 2.92 s to compile during
// evaluation and charged 2 MiB of the 15.5 it allocated.
func TestCaseFoldingIsBounded(t *testing.T) {
	lim := DefaultLimits()
	wide := "(?i)" + strings.Repeat(`[\x{100}-\x{10FFFF}]`, 400)
	if foldCost(wide) < 400*100_000 {
		t.Errorf("foldCost(%.30q…) = %d, want the 400 ranges counted", wide, foldCost(wide))
	}
	start := time.Now()
	if err := compileLiteralRegex(newRegexCache(&lim), wide); err == nil || !strings.Contains(err.Error(), "too complex") {
		t.Errorf("a literal of 400 wide folded classes = %v, want too complex", err)
	}
	m := newTestMeter()
	_, err := matchString(m, "a", wide)
	var ee *evalError
	if !errors.As(err, &ee) {
		t.Errorf("a built pattern of 400 wide folded classes = %v, want an evaluation error", err)
	}
	if d := time.Since(start); d > within(100*time.Millisecond) {
		t.Errorf("refusing it took %v", d)
	}
	// Distinct patterns that each fit: as many as an evaluation allows, compiled on its account.
	m = newTestMeter()
	i := 0
	bounded(t, "distinct folded patterns", m, 3*time.Second, func() error {
		return repeat(func() error {
			i++
			_, err := matchString(m, "a", `(?i)[\x{100}-\x{10FFFF}]`+strings.Repeat("x", i))
			return err
		})
	})
	// \x{41}-\x{1E943} and wider is not folded one by one.
	if n := foldCost(`(?i)[\x{0}-\x{10FFFF}]`); n != 0 {
		t.Errorf("a class covering the folds costs %d", n)
	}
	if n := foldCost(`[\x{100}-\x{10FFFF}]`); n != 0 {
		t.Errorf("a class without (?i) costs %d", n)
	}
}
