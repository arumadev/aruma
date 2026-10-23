package rules

import (
	"container/list"
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"
)

// Patterns are RE2, as the docs require and Go's regexp is (ADR-021 §4). matches() matches the
// whole string (Pyric matches-full-string-regex: 'hello world'.matches('world') is false);
// split() and replace() find every match, as Java's String.split and replaceAll do.
//
// A pattern's cost is bounded before it is compiled. A hostile pattern is cheap to write and
// dear to compile: 27 KB of \pL{1000} compiles to 3,000,002 instructions and 742 MB (ADR-021
// §4). So before regexp.Compile runs:
//
//  1. parseCost estimates what regexp/syntax.Parse allocates from the text alone: about 256
//     bytes for each byte of the pattern, and 48 KiB for each Unicode class (\pL), measured on
//     Go 1.27. A pattern past instructions × 512 bytes is refused unparsed.
//  2. estimateInsts estimates the program from the parsed tree without building it, as
//     syntax.Compile would (x{n} is n copies of x), saturating at the limit.
//  3. syntax.Compile builds the program, now known to be small, and its exact size is checked.
//
// Past the limit, a literal pattern fails compilation and one built during evaluation is an
// evaluation error (ADR-021 §4). Matching charges the string's length times the program's
// instructions to the meter's work; compiling a pattern built during evaluation charges its
// parse to the meter's allocation and its program to both.

// Estimates of the bytes behind a pattern (Go 1.27, measured as the package comment says).
const (
	parseBytesPerByte  = 256
	parseBytesPerClass = 48 << 10
	parseBytesPerInst  = 512 // the budget of parseCost, per instruction of the limit
	progBytesPerInst   = 64  // a compiled program's instruction, twice: whole and partial
)

// compiledRegex is one compiled pattern: anchored for matches(), as written for split and replace.
type compiledRegex struct {
	text   string
	insts  int
	whole  *regexp.Regexp
	part   *regexp.Regexp
	weight int64 // estimated bytes held
}

// regexCache is a ruleset's patterns: the literal ones, compiled once when the file compiles,
// and those built during evaluation, kept up to lim.RegexCacheEntries patterns and
// lim.RegexCacheBytes of estimated programs, the least recently used evicted first. Concurrent
// evaluations share it, under mu.
type regexCache struct {
	mu sync.Mutex

	insts      int // the instruction limit of one program
	maxEntries int
	maxBytes   int64

	literal     map[string]*compiledRegex
	literalSize int64

	built map[string]*list.Element // of *compiledRegex
	lru   *list.List               // front: most recently used
	bytes int64
}

// literalBytes estimates the bytes of the literal patterns' programs, which a ruleset's weight
// counts.
func (rx *regexCache) literalBytes() int64 {
	rx.mu.Lock()
	defer rx.mu.Unlock()
	return rx.literalSize
}

// newRegexCache returns an empty cache bounded by lim.
func newRegexCache(lim *Limits) *regexCache {
	return &regexCache{
		insts:      lim.RegexInstructions,
		maxEntries: lim.RegexCacheEntries,
		maxBytes:   lim.RegexCacheBytes,
		literal:    map[string]*compiledRegex{},
		built:      map[string]*list.Element{},
		lru:        list.New(),
	}
}

// compileLiteralRegex compiles a string literal passed to matches, split or replace when the
// file compiles, and keeps its program in rx. A pattern that is not valid RE2, or whose program
// exceeds the instruction limit (measured with regexp/syntax before regexp.Compile runs), is a
// compilation error with the returned message.
//
// An invalid literal fails compilation even under || true: production (Rules Test API, S0-A),
// and hidane's emulator recordings agree. Pyric recorded 'abc'.replace('(', 'x') as an
// evaluation error; S0-A's measurement on the same API is the later and wins.
func compileLiteralRegex(rx *regexCache, pattern string) error {
	rx.mu.Lock()
	_, ok := rx.literal[pattern]
	rx.mu.Unlock()
	if ok {
		return nil
	}
	p, err := compilePattern(pattern, rx.insts)
	if err != nil {
		return err
	}
	rx.mu.Lock()
	defer rx.mu.Unlock()
	if _, ok := rx.literal[pattern]; !ok {
		rx.literal[pattern] = p
		rx.literalSize += p.weight
	}
	return nil
}

// errTooComplex is the error for a pattern past the limits.
func errTooComplex(pattern string, insts, limit int) *evalError {
	if insts < 0 {
		return errorf("Regular expression is too complex to compile safely: %.40q.", pattern)
	}
	return errorf("Regular expression is too complex to compile safely: %.40q has more than %d instructions.", pattern, limit)
}

// parseCost estimates the bytes regexp/syntax.Parse allocates for text.
func parseCost(text string) int64 {
	classes := strings.Count(text, `\p`) + strings.Count(text, `\P`)
	return int64(len(text))*parseBytesPerByte + int64(classes)*parseBytesPerClass
}

// compilePattern compiles text within limit instructions.
func compilePattern(text string, limit int) (*compiledRegex, *evalError) {
	if parseCost(text) > int64(limit)*parseBytesPerInst {
		return nil, errTooComplex(text, -1, limit)
	}
	re, err := syntax.Parse(text, syntax.Perl)
	if err != nil {
		return nil, errRegex(text)
	}
	if estimateInsts(re, limit) > limit {
		return nil, errTooComplex(text, limit+1, limit)
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return nil, errRegex(text)
	}
	if len(prog.Inst) > limit {
		return nil, errTooComplex(text, len(prog.Inst), limit)
	}
	part, err := regexp.Compile(text)
	if err != nil {
		return nil, errRegex(text)
	}
	whole, err := regexp.Compile(`^(?:` + text + `)$`)
	if err != nil {
		return nil, errRegex(text)
	}
	return &compiledRegex{
		text:   text,
		insts:  len(prog.Inst),
		whole:  whole,
		part:   part,
		weight: int64(len(prog.Inst))*2*progBytesPerInst + 2*int64(len(text)),
	}, nil
}

// estimateInsts estimates the instructions syntax.Compile would make of re after Simplify, which
// copies x{n} n times; past limit it returns limit+1.
func estimateInsts(re *syntax.Regexp, limit int) int {
	sat := func(n int) int { return min(n, limit+1) }
	mul := func(a, b int) int {
		if a != 0 && b > (limit+1)/a {
			return limit + 1
		}
		return sat(a * b)
	}
	sub := func(i int) int { return estimateInsts(re.Sub[i], limit) }
	switch re.Op {
	case syntax.OpLiteral:
		return sat(len(re.Rune))
	case syntax.OpCapture, syntax.OpStar:
		return sat(2 + sub(0))
	case syntax.OpPlus, syntax.OpQuest:
		return sat(1 + sub(0))
	case syntax.OpConcat, syntax.OpAlternate:
		n := len(re.Sub)
		for i := range re.Sub {
			n = sat(n + sub(i))
		}
		return n
	case syntax.OpRepeat:
		s := sub(0)
		if re.Max == -1 {
			return sat(mul(max(re.Min, 1), s) + 2)
		}
		return sat(mul(re.Max, s+1) + 1)
	}
	return 1
}

// get returns the compiled pattern of text, compiling a pattern built during evaluation on the
// meter's account.
func (rx *regexCache) get(m *meter, text string) (*compiledRegex, error) {
	rx.mu.Lock()
	if p, ok := rx.literal[text]; ok {
		rx.mu.Unlock()
		return p, nil
	}
	if e, ok := rx.built[text]; ok {
		rx.lru.MoveToFront(e)
		rx.mu.Unlock()
		return e.Value.(*compiledRegex), nil
	}
	rx.mu.Unlock()

	// Parsing allocates about parseCost; compiling works through the text and the program.
	cost := parseCost(text)
	if cost <= int64(rx.insts)*parseBytesPerInst {
		if err := m.alloc(cost); err != nil {
			return nil, err
		}
	}
	if err := m.work(int64(len(text)) + int64(rx.insts)); err != nil {
		return nil, err
	}
	p, perr := compilePattern(text, rx.insts)
	if perr != nil {
		return nil, perr
	}
	if err := m.alloc(p.weight); err != nil {
		return nil, err
	}

	rx.mu.Lock()
	defer rx.mu.Unlock()
	if e, ok := rx.built[text]; ok { // compiled meanwhile by another evaluation
		return e.Value.(*compiledRegex), nil
	}
	if p.weight > rx.maxBytes || rx.maxEntries < 1 {
		return p, nil // too big to keep
	}
	for rx.lru.Len() >= rx.maxEntries || rx.bytes+p.weight > rx.maxBytes {
		old := rx.lru.Back()
		op := old.Value.(*compiledRegex)
		rx.lru.Remove(old)
		delete(rx.built, op.text)
		rx.bytes -= op.weight
	}
	rx.built[text] = rx.lru.PushFront(p)
	rx.bytes += p.weight
	return p, nil
}

// matchCost is the work of running p over s once.
func matchCost(p *compiledRegex, s string) int64 {
	return (int64(len(s)) + 1) * int64(p.insts)
}

// matchString is s.matches(pattern): the whole of s.
func matchString(m *meter, s, text string) (Value, error) {
	p, err := m.regexes.get(m, text)
	if err != nil {
		return Value{}, err
	}
	if err := m.work(matchCost(p, s)); err != nil {
		return Value{}, err
	}
	return Bool(p.whole.MatchString(s)), nil
}

// findAll returns the matches of p in s with their groups. Go finds them all at once, so the
// number it may find is capped at what the evaluation's remaining allocation can hold, and the
// matches are charged as soon as they are known: what FindAll allocates before the charge is
// bounded by the charge itself.
func findAll(m *meter, p *compiledRegex, s string) ([][]int, error) {
	if err := m.work(2 * matchCost(p, s)); err != nil {
		return nil, err
	}
	per := int64(24 + 16*(p.part.NumSubexp()+1))
	room := (m.lim.AllocBytes - m.allocated) / per
	n := int(min(room, int64(len(s))+1))
	if n < 1 {
		return nil, m.alloc(per)
	}
	found := p.part.FindAllStringSubmatchIndex(s, n+1)
	if err := m.alloc(int64(len(found)) * per); err != nil {
		return nil, err
	}
	return found, nil
}

// splitString is s.split(pattern), as Java's String.split: a match of nothing at the start
// makes no empty first part, and the empty parts at the end are dropped (production, Rules
// Test API, S0-A); with no match, the whole string is the one part.
func splitString(m *meter, s, text string) (Value, error) {
	p, err := m.regexes.get(m, text)
	if err != nil {
		return Value{}, err
	}
	found, err := findAll(m, p, s)
	if err != nil {
		return Value{}, err
	}
	var parts []string
	at := 0
	for _, loc := range found {
		if loc[1] == 0 {
			continue
		}
		parts = append(parts, s[at:loc[0]])
		at = loc[1]
	}
	if at == 0 {
		parts = []string{s}
	} else {
		parts = append(parts, s[at:])
		for len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
	}
	if err := chargeList(m, len(parts)); err != nil {
		return Value{}, err
	}
	if err := m.alloc(int64(len(s))); err != nil {
		return Value{}, err
	}
	out := make([]Value, len(parts))
	for i, part := range parts {
		out[i] = String(part)
	}
	return List(out), nil
}

// replacement is one piece of a replace() replacement: literal text, or a group.
type replacement struct {
	text  string
	group int // -1 for text
}

// parseReplacement reads a replacement as Java's Matcher.appendReplacement does: \ escapes the
// next character, $n is group n (the longest number that names a group), ${name} a named group
// (Pyric stdlib-string-bytes-hashing: '$1$1' and '\$', production).
func parseReplacement(p *compiledRegex, repl string) ([]replacement, error) {
	invalid := errorf("Invalid replacement string: %s.", repl)
	groups := p.part.NumSubexp()
	var out []replacement
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, replacement{text: lit.String(), group: -1})
			lit.Reset()
		}
	}
	for i := 0; i < len(repl); i++ {
		switch c := repl[i]; c {
		case '\\':
			if i+1 >= len(repl) {
				return nil, invalid
			}
			i++
			lit.WriteByte(repl[i])
		case '$':
			if i+1 >= len(repl) {
				return nil, invalid
			}
			flush()
			if repl[i+1] == '{' {
				end := strings.IndexByte(repl[i+2:], '}')
				if end < 0 {
					return nil, invalid
				}
				g := p.part.SubexpIndex(repl[i+2 : i+2+end])
				if g < 0 {
					return nil, invalid
				}
				out = append(out, replacement{group: g})
				i += 2 + end
				continue
			}
			d := repl[i+1]
			if d < '0' || d > '9' || int(d-'0') > groups {
				return nil, invalid
			}
			g := int(d - '0')
			i++
			for i+1 < len(repl) && repl[i+1] >= '0' && repl[i+1] <= '9' {
				next := g*10 + int(repl[i+1]-'0')
				if next > groups {
					break
				}
				g = next
				i++
			}
			out = append(out, replacement{group: g})
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return out, nil
}

// replaceString is s.replace(pattern, replacement): every match replaced, as Java's
// replaceAll. The replacement is read only when there is a match, as Java does.
func replaceString(m *meter, s, text, repl string) (Value, error) {
	p, err := m.regexes.get(m, text)
	if err != nil {
		return Value{}, err
	}
	found, err := findAll(m, p, s)
	if err != nil {
		return Value{}, err
	}
	if len(found) == 0 {
		return String(s), nil
	}
	pieces, err := parseReplacement(p, repl)
	if err != nil {
		return Value{}, err
	}
	group := func(loc []int, g int) string {
		if loc[2*g] < 0 {
			return ""
		}
		return s[loc[2*g]:loc[2*g+1]]
	}
	// Measure, charge, then build.
	size, at := 0, 0
	for _, loc := range found {
		size += loc[0] - at
		for _, r := range pieces {
			if r.group < 0 {
				size += len(r.text)
			} else {
				size += len(group(loc, r.group))
			}
		}
		at = loc[1]
	}
	size += len(s) - at
	if err := chargeString(m, size); err != nil {
		return Value{}, err
	}
	var b strings.Builder
	b.Grow(size)
	at = 0
	for _, loc := range found {
		b.WriteString(s[at:loc[0]])
		for _, r := range pieces {
			if r.group < 0 {
				b.WriteString(r.text)
			} else {
				b.WriteString(group(loc, r.group))
			}
		}
		at = loc[1]
	}
	b.WriteString(s[at:])
	return String(b.String()), nil
}
