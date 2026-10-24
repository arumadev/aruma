package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// storageFile returns a Storage file whose match block holds lines.
func storageFile(lines []string) string {
	return "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} {\n" +
		strings.Join(lines, "\n") + "\n    }\n  }\n}\n"
}

// heavyPatterns returns n allow statements, each matching a distinct literal pattern of about
// 9,000 instructions, near the limit of one program: the review's SR3-02 file.
func heavyPatterns(n int) []string {
	lines := make([]string, n)
	for k := range n {
		lines[k] = fmt.Sprintf("      allow read: if 'x'.matches('a{1000}b{1000}c{1000}d{1000}e{1000}f{1000}g{1000}h{1000}i{1000}j{%d}');", k+1)
	}
	return lines
}

// SR3-02: a hundred distinct literal patterns in 10 KB of source took 184 ms and 206 MiB to
// compile (811 MiB allocated), and the ruleset weighed 170 MiB, more than the cache of
// compiled rulesets, so every request compiled it again. regex_bytes refuses the file at the
// first pattern past the budget, the patterns after it uncompiled.
func TestLiteralPatternsHaveABudget(t *testing.T) {
	lim := DefaultLimits()
	src := storageFile(heavyPatterns(100))
	if len(src) > 12<<10 {
		t.Fatalf("the file is %d bytes, not the review's 10 KB", len(src))
	}
	var rs *Ruleset
	var issues Issues
	start := time.Now()
	mem := allocated(func() { rs, issues = Compile(Storage, File{Name: "storage.rules", Content: src}, lim) })
	d := time.Since(start)
	t.Logf("100 patterns: %v, allocated %d MiB, %d issues", d, mem>>20, len(issues))
	if rs != nil {
		t.Fatalf("compiled, weighing %d bytes", rs.Weight())
	}
	var errs []Issue
	for _, is := range issues {
		if is.Severity == SeverityError {
			errs = append(errs, is)
		}
	}
	// A program of 9,000 instructions is estimated at 1.75 MB: two fit in 4 MiB, the third does not.
	want := "Regular expressions are too complex to compile safely: with \"a{1000}b{1000}c{1000}d{1000}e{1000}f{100\", the patterns of the file would take more than 4194304 bytes."
	if len(errs) != 1 || errs[0].Description != want {
		t.Fatalf("errors %v, want one: %s", errs, want)
	}
	if p := errs[0].Position; p == nil || p.Line != 7 || p.Column != len("      allow read: if 'x'.matches(")+1 {
		t.Errorf("at %+v, want the third pattern's literal, L7:%d", p, len("      allow read: if 'x'.matches(")+1)
	}
	if d > within(time.Second) {
		t.Errorf("took %v", d)
	}
	if !raceEnabled && mem > 64<<20 {
		t.Errorf("allocated %d MiB", mem>>20)
	}

	// Within the budget the file compiles, and weighs what the cache keeps four of.
	rs, issues = Compile(Storage, File{Name: "storage.rules", Content: storageFile(heavyPatterns(2))}, lim)
	if rs == nil {
		t.Fatalf("two patterns: %v", issues)
	}
	if w := rs.Weight(); w > lim.CompiledRulesetsBytes/rulesetShare || w < 2*(1<<20) {
		t.Errorf("two patterns weigh %d, past a quarter of %d or below their programs", w, lim.CompiledRulesetsBytes)
	}

	// A pattern repeated is compiled and counted once.
	repeated := heavyPatterns(1)
	rs, issues = Compile(Storage, File{Name: "storage.rules", Content: storageFile(append(repeated, repeated[0], repeated[0], repeated[0]))}, lim)
	if rs == nil {
		t.Errorf("one pattern four times: %v", issues)
	}

	// A lower budget refuses less.
	lim.RegexBytes = 1 << 10
	if rs, issues := Compile(Storage, File{Name: "storage.rules", Content: storageFile([]string{"      allow read: if 'x'.matches('[a-z]{100}');"})}, lim); rs != nil || issues.Err() == nil {
		t.Errorf("a pattern of 100 classes under regex_bytes 1 KiB: compiled %v", issues)
	}
}

// What compiles fits in the cache of compiled rulesets: a file whose tree weighs more than a
// quarter of compiled_rulesets_bytes does not compile, with an error about the whole file.
func TestRulesetWeightIsBounded(t *testing.T) {
	lim := DefaultLimits()
	lim.RegexCacheBytes, lim.RegexBytes = 1, 1
	lim.CompiledRulesetsBytes = rulesetShare * lim.leastWeight()
	if err := lim.Validate(); err != nil {
		t.Fatal(err)
	}
	lines := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "      allow read: if true;"
		}
		return out
	}
	rs, issues := Compile(Storage, File{Name: "storage.rules", Content: storageFile(lines(10))}, lim)
	if rs == nil {
		t.Fatalf("ten statements: %v", issues)
	}
	if rs.Weight() > lim.rulesetBytes() {
		t.Errorf("weighs %d, past %d", rs.Weight(), lim.rulesetBytes())
	}
	rs, issues = Compile(Storage, File{Name: "storage.rules", Content: storageFile(lines(3000))}, lim)
	if rs != nil {
		t.Fatalf("3,000 statements compile, weighing %d of %d", rs.Weight(), lim.rulesetBytes())
	}
	is := issues[0]
	if is.Position != nil || is.Severity != SeverityError || !strings.HasPrefix(is.Description, "The rules are too large to keep compiled: they would take about ") {
		t.Errorf("first issue %+v", is)
	}
}
