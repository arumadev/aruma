package rules

import "sync"

// regexCache is a ruleset's patterns: the literal ones, compiled once when the file compiles,
// and those built during evaluation, kept up to lim.RegexCacheEntries patterns and
// lim.RegexCacheBytes of estimated programs. Concurrent evaluations share it, under mu.
// S1-B fills it in.
type regexCache struct {
	mu sync.Mutex
}

// literalBytes estimates the bytes of the literal patterns' programs, which a ruleset's weight
// counts. S1-B replaces this provisional answer, which is right while no program is kept.
func (rx *regexCache) literalBytes() int64 { return 0 }

// newRegexCache returns an empty cache bounded by lim.
func newRegexCache(lim *Limits) *regexCache {
	return &regexCache{}
}

// compileLiteralRegex compiles a string literal passed to matches, split or replace when the
// file compiles, and keeps its program in rx. A pattern that is not valid RE2, or whose program
// exceeds the instruction limit (measured with regexp/syntax before regexp.Compile runs), is a
// compilation error with the returned message. S1-B replaces this stub.
func compileLiteralRegex(rx *regexCache, pattern string) error {
	panic("S1-B: compileLiteralRegex")
}
