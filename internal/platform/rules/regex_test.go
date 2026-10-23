package rules

import (
	"errors"
	"fmt"
	"regexp/syntax"
	"strings"
	"sync"
	"testing"
)

func TestMatches(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric matches-full-string-regex, string-literals-and-regex, matches-regex,
		// upload-primitives-boundaries: the whole string.
		{"no partial match", vCall(vs("hello world"), "matches", vs("world")), vb(false), ""},
		{"exact", vCall(vs("hello"), "matches", vs("hello")), vb(true), ""},
		{".*world", vCall(vs("hello world"), "matches", vs(".*world")), vb(true), ""},
		{"full consume", vCall(vs("abc123"), "matches", vs("[a-z]+[0-9]+")), vb(true), ""},
		{"a trailing character", vCall(vs("abc123!"), "matches", vs("[a-z]+[0-9]+")), vb(false), ""},
		{"an email", vCall(vs("alice@acme.com"), "matches", vs(`[a-z]+@acme\.com`)), vb(true), ""},
		{"a substring would match", vCall(vs("1alice@acme.com"), "matches", vs(`[a-z]+@acme\.com`)), vb(false), ""},
		{"image/.*", vCall(vs("image/png"), "matches", vs("image/.*")), vb(true), ""},
		{"prefixed", vCall(vs("ximage/png"), "matches", vs("image/.*")), vb(false), ""},
		{"a suffix defeats it", vCall(vs("image/jpeg; x"), "matches", vs("image/jpeg")), vb(false), ""},
		{"alternation is grouped", vCall(vs("ab"), "matches", vs("a|ab")), vb(true), ""},
		{"a tab", vCall(vs("a\tb"), "matches", vs(`.*\t.*`)), vb(true), ""},
		// RE2: \d is ASCII.
		{"\\d is ASCII", vCall(vs("٣"), "matches", vs(`\d`)), vb(false), ""},
		{". is a character, not a byte", vCall(vs("é"), "matches", vs(".")), vb(true), ""},
		{"an invalid built pattern", vCall(vs("a"), "matches", vs("(")), Value{}, "Invalid regular expression pattern. Pattern: (."},
		{"backreferences are not RE2", vCall(vs("aa"), "matches", vs(`(a)\1`)), Value{}, "Invalid regular expression pattern."},
	})
}

func TestSplit(t *testing.T) {
	runValueCases(t, []valueCase{
		{"split", vCall(vs("a/b/c"), "split", vs("/")), vl(vs("a"), vs("b"), vs("c")), ""},
		{"a pattern", vCall(vs("a1b22c"), "split", vs("[0-9]+")), vl(vs("a"), vs("b"), vs("c")), ""},
		{"a dot is a pattern", vCall(vs("a.b"), "split", vs(".")), vl(), ""},
		{"an escaped dot", vCall(vs("a.b"), "split", vs("[.]")), vl(vs("a"), vs("b")), ""},
		// production (Rules Test API, S0-A): split() drops trailing empty strings, as Java does.
		{"trailing empties dropped", vCall(vs("a,b,,"), "split", vs(",")), vl(vs("a"), vs("b")), ""},
		{"only separators", vCall(vs(",,"), "split", vs(",")), vl(), ""},
		{"a leading empty part stays", vCall(vs(",a"), "split", vs(",")), vl(vs(""), vs("a")), ""},
		{"inner empties stay", vCall(vs("a,,b"), "split", vs(",")), vl(vs("a"), vs(""), vs("b")), ""},
		{"no match: the whole string", vCall(vs("abc"), "split", vs(",")), vl(vs("abc")), ""},
		{"the empty string", vCall(vs(""), "split", vs(",")), vl(vs("")), ""},
		// Java: a match of nothing at the start makes no empty first part.
		{"split into characters", vCall(vs("abc"), "split", vs("")), vl(vs("a"), vs("b"), vs("c")), ""},
		{"split(1)", vCall(vs("a"), "split", vi(1)), Value{}, "Received: string.split(int). Expected: string.split(string)."},
		{"an invalid pattern", vCall(vs("a"), "split", vs("[")), Value{}, "Invalid regular expression pattern. Pattern: [."},
	})
}

func TestReplace(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric stdlib-string-bytes-hashing and simulator-language-semantics (production).
		{"every match", vCall(vs("aXa"), "replace", vs("a"), vs("b")), vs("bXb"), ""},
		{"a regular expression", vCall(vs("a.b"), "replace", vs("."), vs("-")), vs("---"), ""},
		{"a character class", vCall(vs("a1b22"), "replace", vs("[0-9]+"), vs("#")), vs("a#b#"), ""},
		{"a numbered group", vCall(vs("ab"), "replace", vs("(a)"), vs("$1$1")), vs("aab"), ""},
		{"an escaped dollar", vCall(vs("ab"), "replace", vs("a"), vs(`\$`)), vs("$b"), ""},
		{"an escaped dollar mid-string", vCall(vs("abc"), "replace", vs("b"), vs(`\$`)), vs("a$c"), ""},
		{"the longest group number that exists", vCall(vs("ab"), "replace", vs("(a)"), vs("$10")), vs("a0b"), ""},
		{"a named group", vCall(vs("ab"), "replace", vs("(?P<x>a)"), vs("[${x}]")), vs("[a]b"), ""},
		{"group zero", vCall(vs("ab"), "replace", vs("b"), vs("<$0>")), vs("a<b>"), ""},
		{"no match leaves the string", vCall(vs("abc"), "replace", vs("z"), vs("$9")), vs("abc"), ""},
		{"a missing group", vCall(vs("ab"), "replace", vs("a"), vs("$1")), Value{}, "Invalid replacement string: $1."},
		{"a dangling dollar", vCall(vs("ab"), "replace", vs("a"), vs("x$")), Value{}, "Invalid replacement string"},
		{"a dangling backslash", vCall(vs("ab"), "replace", vs("a"), vs(`x\`)), Value{}, "Invalid replacement string"},
		{"an unknown name", vCall(vs("ab"), "replace", vs("a"), vs("${n}")), Value{}, "Invalid replacement string"},
		{"an empty match everywhere", vCall(vs("ab"), "replace", vs(""), vs("-")), vs("-a-b-"), ""},
		{"an invalid pattern", vCall(vs("abc"), "replace", vs("("), vs("x")), Value{}, "Invalid regular expression pattern. Pattern: (."},
	})
}

// A literal pattern compiles once with the file: an invalid one or one past the instruction
// limit fails compilation, and its program counts in the ruleset's weight.
func TestCompileLiteralRegex(t *testing.T) {
	lim := DefaultLimits()
	rx := newRegexCache(&lim)
	if err := compileLiteralRegex(rx, `[a-z]+@acme\.com`); err != nil {
		t.Fatal(err)
	}
	if rx.literalBytes() <= 0 {
		t.Error("a literal pattern's program weighs nothing")
	}
	rs := &Ruleset{src: "x", lim: lim, regexes: rx}
	if rs.Weight() <= 1+lim.RegexCacheBytes {
		t.Error("the ruleset's weight leaves out its literal patterns")
	}
	before := rx.literalBytes()
	if err := compileLiteralRegex(rx, `[a-z]+@acme\.com`); err != nil || rx.literalBytes() != before {
		t.Errorf("compiling a literal twice = %v, weight %d then %d", err, before, rx.literalBytes())
	}
	// production (Rules Test API, S0-A): a literal RE2 rejects fails compilation, even where
	// || true would absorb an evaluation error.
	for _, bad := range []string{"(", "[", `(a)\1`, "a**", `\pX`} {
		err := compileLiteralRegex(rx, bad)
		if err == nil || !strings.Contains(err.Error(), "Invalid regular expression pattern. Pattern: "+bad+".") {
			t.Errorf("literal %q = %v, want the invalid pattern error", bad, err)
		}
	}
	for _, big := range []string{strings.Repeat("a", 10_001), "a{1000}b{1000}c{1000}d{1000}e{1000}f{1000}g{1000}h{1000}i{1000}j{1000}k{1000}"} {
		if err := compileLiteralRegex(rx, big); err == nil || !strings.Contains(err.Error(), "too complex") {
			t.Errorf("literal %.20q… = %v, want too complex", big, err)
		}
	}
	// A literal is found at evaluation without compiling again.
	m := newMeter(&lim, NewInflight(lim.InflightBytes), rx)
	if v, err := matchString(m, "bob@acme.com", `[a-z]+@acme\.com`); err != nil || !v.boolean() {
		t.Errorf("matches with the literal = %s, %v", showValue(v), err)
	}
	if m.allocated != 0 {
		t.Errorf("a literal pattern charged %d bytes at evaluation", m.allocated)
	}
}

// The patterns built during evaluation are kept up to the cache's entries and bytes, the least
// recently used evicted first, and each is charged to the evaluation that compiles it.
func TestRegexCache(t *testing.T) {
	lim := DefaultLimits()
	lim.RegexCacheEntries = 3
	rx := newRegexCache(&lim)
	m := newMeter(&lim, NewInflight(lim.InflightBytes), rx)
	for i := range 5 {
		if _, err := matchString(m, "x", fmt.Sprintf("x%d?", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(rx.built) != 3 || rx.lru.Len() != 3 {
		t.Fatalf("the cache holds %d patterns, want 3", len(rx.built))
	}
	for _, gone := range []string{"x0?", "x1?"} {
		if _, ok := rx.built[gone]; ok {
			t.Errorf("%s was not evicted", gone)
		}
	}
	charged := m.allocated
	if _, err := matchString(m, "x", "x4?"); err != nil || m.allocated != charged {
		t.Errorf("a cached pattern charged %d bytes, %v", m.allocated-charged, err)
	}
	// Bytes bound the cache too.
	lim.RegexCacheEntries, lim.RegexCacheBytes = 100, 1
	small := newRegexCache(&lim)
	m = newMeter(&lim, NewInflight(lim.InflightBytes), small)
	if _, err := matchString(m, "x", "x"); err != nil || len(small.built) != 0 {
		t.Errorf("a pattern heavier than the cache was kept (%d), %v", len(small.built), err)
	}
}

// Concurrent evaluations share a ruleset's cache (run under -race).
func TestRegexCacheConcurrent(t *testing.T) {
	lim := DefaultLimits()
	lim.RegexCacheEntries = 8
	rx := newRegexCache(&lim)
	if err := compileLiteralRegex(rx, "lit.*"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			m := newMeter(&lim, NewInflight(lim.InflightBytes), rx)
			for i := range 50 {
				pat := fmt.Sprintf("p%d[0-9]*", (g+i)%12)
				v, err := matchString(m, fmt.Sprintf("p%d123", (g+i)%12), pat)
				if err != nil || !v.boolean() {
					t.Errorf("%s: %s, %v", pat, showValue(v), err)
					return
				}
				if v, err := matchString(m, "literal", "lit.*"); err != nil || !v.boolean() {
					t.Errorf("literal: %s, %v", showValue(v), err)
					return
				}
			}
			m.release()
		})
	}
	wg.Wait()
	if len(rx.built) > 8 {
		t.Errorf("the cache holds %d patterns past its 8", len(rx.built))
	}
}

// A built pattern past the instruction limit is an evaluation error, not an abort (ADR-021 §4),
// so || true absorbs it.
func TestBuiltPatternTooComplex(t *testing.T) {
	m := newTestMeter()
	_, err := matchString(m, "a", strings.Repeat("a", 10_001))
	var ee *evalError
	if !errors.As(err, &ee) || !strings.Contains(ee.msg, "too complex") {
		t.Errorf("a built pattern past the limit = %v, want an evaluation error", err)
	}
}

func TestEstimateInsts(t *testing.T) {
	lim := DefaultLimits()
	for _, p := range []string{"a", "abc", "a|b|c", "(a)*", "a+", "a?", "a{3}", "a{2,5}", "a{2,}", "(ab){3}",
		"[a-z]+@[a-z]+\\.com", `\pL{100}`, "(a|b){10,20}c*", "^x$", "(?i)abc", "(?:)", "a{0}"} {
		re := mustParse(t, p)
		prog := mustCompile(t, re)
		if est := estimateInsts(re, lim.RegexInstructions) + 2; est < prog {
			t.Errorf("%q: estimate %d below the program's %d instructions", p, est, prog)
		}
	}
}

func mustParse(t *testing.T, p string) *syntax.Regexp {
	t.Helper()
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

func mustCompile(t *testing.T, re *syntax.Regexp) int {
	t.Helper()
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		t.Fatal(err)
	}
	return len(prog.Inst)
}
