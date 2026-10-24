package rules

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// held returns the heap that f leaves behind, after a collection.
func held(f func()) int64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	return int64(after.HeapAlloc) - int64(before.HeapAlloc)
}

// The classes the weight of a pattern must count (the review of S7-B1, SR3-02-class-tables): a
// class is one instruction, and its table of runes is kept in each of the three forms. Counted
// by instructions alone, 185 patterns of fifty \pL each in 44 KB compiled within regex_bytes
// and the share, weighing 24.9 MB and holding 300 MiB; the cache of patterns built during
// evaluation held 303 MiB under its 4 MiB.
var classShapes = []string{
	strings.Repeat(`\pL`, 50),
	strings.Repeat(`\PL`, 50),
	strings.Repeat(`\p{Han}`, 50),
	`[^\pL]{20}`,
	`(?i)` + strings.Repeat(`\pL`, 20),
	`(?i)` + strings.Repeat(`[a-zα-ωа-я]`, 50),
	`\pL{50}`,
	`(?:\pL|\pN){50}`,
	`a{1000}b{1000}c{1000}d{1000}e{1000}f{1000}g{1000}h{1000}i{1000}j`,
}

// classFile returns a Storage file of n statements, each matching a distinct literal pattern of
// shape.
func classFile(shape string, n int) string {
	lines := make([]string, n)
	for k := range n {
		lines[k] = fmt.Sprintf("      allow read: if 'x'.matches('%d%s');", k, strings.ReplaceAll(shape, `\`, `\\`))
	}
	return storageFile(lines)
}

// For each shape, the most patterns regex_bytes admits: the file compiles, what it holds is at
// most twice what its weight counts of it, and still once its literal patterns keep their forms
// for lone surrogates in every block they may.
func TestClassTablesAreCounted(t *testing.T) {
	if raceEnabled {
		t.Skip("what the heap holds is measured without the race detector")
	}
	lim := DefaultLimits()
	// The review's file: refused at its second pattern, which takes it past regex_bytes.
	_, issues := Compile(Storage, File{Name: "storage.rules", Content: classFile(classShapes[0], 185)}, lim)
	if first, _ := firstErrorOf(issues); first.Position == nil || first.Position.Line != 6 ||
		!strings.HasPrefix(first.Description, "Regular expressions are too complex") {
		t.Errorf("185 patterns of fifty \\pL: %v", first)
	}
	for _, shape := range classShapes {
		// The first statement is on line 5: a file refused at line L holds L-5 patterns that fit.
		n := min(400, 250_000/len(classFile(shape, 1)))
		if _, issues := Compile(Storage, File{Name: "storage.rules", Content: classFile(shape, n)}, lim); issues.Err() != nil {
			first, _ := firstErrorOf(issues)
			if first.Position == nil || !strings.HasPrefix(first.Description, "Regular expressions are too complex") {
				t.Fatalf("%.20s: %v", shape, first)
			}
			n = first.Position.Line - 5
		}
		if n < 1 {
			t.Errorf("%.20s: not one pattern fits regex_bytes", shape)
			continue
		}
		var rs *Ruleset
		var issues Issues
		mem := held(func() { rs, issues = Compile(Storage, File{Name: "storage.rules", Content: classFile(shape, n)}, lim) })
		if rs == nil {
			t.Fatalf("%.20s, %d patterns: %v", shape, n, issues.Err())
		}
		// What the budgets count of the file: its text, its tree, its patterns' programs.
		counted := int64(len(rs.src)) + rs.treeBytes + rs.regexes.literalBytes()
		lone := held(func() { fillLoneForms(t, rs) })
		t.Logf("%-24.24q %3d patterns: counted %8d, weight %8d, held %8d (%.2f of counted), with the lone forms %8d (%.2f of weight)",
			shape, n, counted, rs.Weight(), mem, float64(mem)/float64(counted), mem+lone, float64(mem+lone)/float64(rs.Weight()))
		if rs.regexes.literalBytes() > lim.RegexBytes {
			t.Errorf("%.20s: literal patterns of %d past regex_bytes", shape, rs.regexes.literalBytes())
		}
		if mem > 2*counted || mem+lone > 2*rs.Weight() {
			t.Errorf("%.20s: holds %d, with the lone forms %d; counted %d, weight %d", shape, mem, mem+lone, counted, rs.Weight())
		}
		runtime.KeepAlive(rs)
	}

	// The cache of patterns built during evaluation holds no more than twice regex_cache_bytes,
	// lone forms included, which it does not keep.
	rx := newRegexCache(&lim)
	in := NewInflight(lim.InflightBytes)
	lone := "\xed\xa0\x80" // U+D800, alone
	mem := held(func() {
		for k := range 256 {
			m := newMeter(&lim, in, rx)
			if _, err := matchString(m, lone, fmt.Sprintf("%d%s", k, classShapes[3])); isAbort(err) {
				t.Fatalf("pattern %d: %v", k, err)
			}
			m.release()
		}
	})
	t.Logf("built: %d patterns kept, counted %d of %d, held %d", rx.lru.Len(), rx.bytes, lim.RegexCacheBytes, mem)
	if mem > 2*lim.RegexCacheBytes {
		t.Errorf("the cache of built patterns holds %d, counted %d", mem, rx.bytes)
	}
	runtime.KeepAlive(rx)
}

// fillLoneForms matches every literal pattern of rs against strings with a lone surrogate whose
// sentinels each take another block, so that each pattern keeps its forms for the most blocks.
func fillLoneForms(t *testing.T, rs *Ruleset) {
	t.Helper()
	in := NewInflight(rs.lim.InflightBytes)
	for text := range rs.regexes.literal {
		s := "\xed\xa0\x80"
		for _, base := range sentinelBlocks[:loneCacheBlocks] {
			m := newMeter(&rs.lim, in, rs.regexes)
			// A pattern whose rewritten form is too complex fails as a value, and keeps no form.
			if _, err := matchString(m, s, text); isAbort(err) {
				t.Fatalf("%.20s on a lone surrogate: %v", text, err)
			}
			m.release()
			s += string(base) // the next string's sentinels take the next block
		}
	}
}

// firstErrorOf returns the first error among issues.
func firstErrorOf(is Issues) (Issue, bool) {
	for _, i := range is {
		if i.Severity == SeverityError {
			return i, true
		}
	}
	return Issue{}, false
}
