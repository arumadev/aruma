package rules

import (
	"container/list"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Patterns are RE2, as the docs require and Go's regexp is (ADR-021 §4). matches() matches the
// whole string (Pyric matches-full-string-regex: 'hello world'.matches('world') is false);
// split() and replace() find every match as RE2J does, production's matcher (matcher, below).
// The matcher reads a lone surrogate as U+FFFD (wtf8.go regexView).
//
// A pattern's cost is bounded before it is compiled. A hostile pattern is cheap to write and
// dear to compile: 27 KB of \pL{1000} compiles to 3,000,002 instructions and 742 MB (ADR-021
// §4). So before regexp.Compile runs:
//
//  1. parseCost estimates what regexp/syntax.Parse allocates from the text alone: about 256
//     bytes for each byte of the pattern, 48 KiB for each Unicode class (\pL), and, where (?i)
//     folds case, 16 bytes for each character a class range folds one by one, measured on Go
//     1.27 (see foldCost). A pattern past instructions × 256 bytes is refused unparsed.
//  2. estimateInsts estimates the program from the parsed tree without building it, as
//     syntax.Compile would (x{n} is n copies of x), saturating at the limit.
//  3. syntax.Compile builds the program, now known to be small, and its exact size is checked.
//
// Past the limit, a literal pattern fails compilation and one built during evaluation is an
// evaluation error (ADR-021 §4). A pattern is parsed four times, once here and once by each of
// the three regexp.Compile calls (Go cannot compile a parsed tree), and a pattern built during
// evaluation is charged all four: its parses and programs to the meter's allocation, and the
// work of them. Matching charges the string's length times the program's instructions.

// Estimates of the bytes behind a pattern (Go 1.27, measured as the comment above says).
const (
	parseBytesPerByte  = 256
	parseBytesPerClass = 48 << 10
	parseBytesPerFold  = 16
	parseBytesPerInst  = 256 // the budget of parseCost, per instruction of the limit
	progBytesPerInst   = 64  // a compiled program's instruction
	compiles           = 4   // syntax.Parse, and regexp.Compile of whole, first and next
)

// compiledRegex is one compiled pattern, in the three forms the library runs.
type compiledRegex struct {
	text   string
	insts  int            // the instructions of the pattern's own program
	groups int            // its capture groups
	whole  *regexp.Regexp // ^(?:p)$, for matches()
	first  *regexp.Regexp // \A(?s:.*?)(p), the first search of split() and replace()
	next   *regexp.Regexp // \A(?s:.)(?s:.*?)(p), each later one, a character of context first
	weight int64          // estimated bytes held
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
// An invalid literal fails compilation even under || true: production (Rules Test API, S0-A;
// the review's probes: '(a)\1' and 'a(?=b)b' fail compilation), and hidane's emulator
// recordings agree. Pyric recorded 'abc'.replace('(', 'x') as an evaluation error; S0-A's
// measurement on the same API is the later and wins.
func compileLiteralRegex(rx *regexCache, pattern string) error {
	rx.mu.Lock()
	_, ok := rx.literal[pattern]
	rx.mu.Unlock()
	if ok {
		return nil
	}
	p, err := compilePattern(pattern, rx.insts, nil)
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

// parseCost estimates the bytes one regexp/syntax.Parse of text allocates.
func parseCost(text string) int64 {
	classes := strings.Count(text, `\p`) + strings.Count(text, `\P`)
	return int64(len(text))*parseBytesPerByte + int64(classes)*parseBytesPerClass +
		foldCost(text)*parseBytesPerFold
}

// Go folds a class range under (?i) one character at a time between these two, unless the
// range covers both (regexp/syntax appendFoldedRange): (?i)[\x{100}-\x{10FFFF}] folds 124,999
// characters, 2.6 ms and 13 KB each time it is parsed.
const (
	minFold = 0x0041
	maxFold = 0x1E943
)

// foldCost counts the characters Go folds one by one in the classes of text, when a (?i) flag
// appears anywhere in it: an over-estimate, since it does not tell which classes the flag
// covers.
func foldCost(text string) int64 {
	if !hasFoldFlag(text) {
		return 0
	}
	var n int64
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			i++ // an escaped character outside a class
		case '[':
			var cost int64
			cost, i = classFoldCost(text, i+1)
			n += cost
		}
	}
	return n
}

// hasFoldFlag reports whether text sets the i flag: (?i), (?i:…), (?mi-s)…
func hasFoldFlag(text string) bool {
	for i := 0; i+2 < len(text); i++ {
		if text[i] != '(' || text[i+1] != '?' {
			continue
		}
		for j := i + 2; j < len(text); j++ {
			c := text[j]
			if c == 'i' {
				return true
			}
			if c == '-' || c == ':' || c == ')' || !(c >= 'a' && c <= 'z' || c == 'U') {
				break
			}
		}
	}
	return false
}

// classFoldCost reads a class from just after its [ and returns the characters its ranges fold
// one by one, and the index of its closing ].
func classFoldCost(text string, i int) (int64, int) {
	if i < len(text) && text[i] == '^' {
		i++
	}
	var n int64
	first := true
	for i < len(text) {
		if text[i] == ']' && !first {
			return n, i
		}
		first = false
		if strings.HasPrefix(text[i:], "[:") {
			if end := strings.Index(text[i+2:], ":]"); end >= 0 {
				i += end + 4
				continue
			}
		}
		lo, next, ok := classChar(text, i)
		if !ok {
			i = next
			continue
		}
		i = next
		hi := lo
		if i+1 < len(text) && text[i] == '-' && text[i+1] != ']' {
			if h, after, ok := classChar(text, i+1); ok {
				hi, i = h, after
			}
		}
		if lo <= minFold && hi >= maxFold || hi < minFold || lo > maxFold {
			continue
		}
		n += int64(min(hi, maxFold) - max(lo, minFold) + 1)
	}
	return n, i
}

// classChar reads one character of a class at i: itself, or an escape of one (\x41, \x{41},
// \101, \]); ok is false for an escape of a class (\d, \pL) and for anything unread.
func classChar(text string, i int) (rune, int, bool) {
	if text[i] != '\\' {
		r, n := utf8.DecodeRuneInString(text[i:])
		return r, i + n, true
	}
	if i+1 >= len(text) {
		return 0, i + 1, false
	}
	c := text[i+1]
	switch {
	case c == 'x':
		if i+2 < len(text) && text[i+2] == '{' {
			if end := strings.IndexByte(text[i+3:], '}'); end >= 0 {
				v, err := strconv.ParseUint(text[i+3:i+3+end], 16, 32)
				return rune(v), i + 4 + end, err == nil
			}
			return 0, i + 2, false
		}
		if i+4 <= len(text) {
			v, err := strconv.ParseUint(text[i+2:i+4], 16, 8)
			return rune(v), i + 4, err == nil
		}
		return 0, i + 2, false
	case c >= '0' && c <= '7':
		j := i + 1
		for j < len(text) && j < i+4 && text[j] >= '0' && text[j] <= '7' {
			j++
		}
		v, _ := strconv.ParseUint(text[i+1:j], 8, 32)
		return rune(v), j, true
	case c == 'p' || c == 'P':
		if i+2 < len(text) && text[i+2] == '{' {
			if end := strings.IndexByte(text[i+3:], '}'); end >= 0 {
				return 0, i + 4 + end, false
			}
		}
		return 0, i + 3, false
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		return 0, i + 2, false // \d, \w, \s, \t, \n…: a class or a control character, cheap
	}
	r, n := utf8.DecodeRuneInString(text[i+1:])
	return r, i + 1 + n, true
}

// compilePattern compiles text within limit instructions. charge, when set, is charged before
// each step allocates: the four parses before the first, the three programs once their size is
// known.
func compilePattern(text string, limit int, charge func(alloc, work int64) error) (*compiledRegex, error) {
	parse := parseCost(text)
	if parse > int64(limit)*parseBytesPerInst {
		return nil, errTooComplex(text, -1, limit)
	}
	if charge != nil {
		if err := charge(compiles*parse, compiles*(int64(len(text))+foldCost(text))); err != nil {
			return nil, err
		}
	}
	re, err := syntax.Parse(text, syntax.Perl)
	if err != nil {
		return nil, errRegex(text)
	}
	est := estimateInsts(re, limit) + 2 // and the program's fail and match
	if est > limit {
		return nil, errTooComplex(text, limit+1, limit)
	}
	weight := 3*int64(est+8)*progBytesPerInst + 3*int64(len(text))
	if charge != nil {
		if err := charge(weight, 4*int64(est)); err != nil {
			return nil, err
		}
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return nil, errRegex(text)
	}
	if len(prog.Inst) > limit {
		return nil, errTooComplex(text, len(prog.Inst), limit)
	}
	p := &compiledRegex{text: text, insts: len(prog.Inst), groups: re.MaxCap(), weight: weight}
	for _, c := range []struct {
		re   **regexp.Regexp
		form string
	}{
		{&p.whole, `^(?:` + text + `)$`},
		{&p.first, `\A(?s:.*?)(` + text + `)`},
		{&p.next, `\A(?s:.)(?s:.*?)(` + text + `)`},
	} {
		if *c.re, err = regexp.Compile(c.form); err != nil {
			return nil, errRegex(text)
		}
	}
	return p, nil
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
		// x{n,} is n copies and a loop; x{n,m} n copies and m-n optional ones, each a split.
		if re.Max == -1 {
			return sat(mul(max(re.Min, 1), s) + 2)
		}
		return sat(mul(re.Max, s) + re.Max - re.Min + 1)
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

	p, err := compilePattern(text, rx.insts, func(alloc, work int64) error {
		if err := m.work(work); err != nil {
			return err
		}
		return m.alloc(alloc)
	})
	if err != nil {
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

// matchString is s.matches(pattern): the whole of s.
func matchString(m *meter, s, text string) (Value, error) {
	p, err := m.regexes.get(m, text)
	if err != nil {
		return Value{}, err
	}
	if err := m.work((int64(len(s)) + 1) * int64(p.insts)); err != nil {
		return Value{}, err
	}
	in, err := viewOf(m, s)
	if err != nil {
		return Value{}, err
	}
	return Bool(p.whole.MatchString(in)), nil
}

// viewOf is regexView(s), its copy charged.
func viewOf(m *meter, s string) (string, error) {
	if err := m.work(scanWork(len(s))); err != nil {
		return "", err
	}
	if !hasLoneSurrogate(s) {
		return s, nil
	}
	if err := m.alloc(int64(len(s))); err != nil {
		return "", err
	}
	return regexView(s), nil
}

// A found match: where it starts and ends, and where each of the pattern's groups does (an
// unset group starts at -1).
type found struct {
	start, end unitPos
	groups     []unitPos // 2 per group of the pattern, from group 1
}

// matcher iterates the matches of a pattern in a string as RE2J's Matcher.find does, which
// production's split() and replace() follow (the review's probes of the Rules Test API,
// 2026-10-24, and S0-A's oracle): each search starts where the last match ended, and after an
// empty match one UTF-16 unit further, so an empty match right after a match counts
// ('abbc'.replace('b*', '-') == '-a--c-'), and empty matches fall between the halves of a pair
// ('a😀b'.split(”).size() == 5). Go's FindAll differs on both: it drops an empty match that
// abuts the last match, and steps a whole character. RE2J is BSD-licensed; this follows its
// design (Matcher.find, Pattern.split) without its code.
//
// Go's regexp cannot start a search in the middle of a string with what comes before in view
// (^, \b), so each search runs p.next, \A(?s:.)(?s:.*?)(pattern), on the string from one
// character before the start: the leading character is the context, and the lazy prefix finds
// the leftmost match, by the pattern's own priorities, as an unanchored search would. In the
// middle of a pair the search runs over U+FFFD for each half (wtf8.go) and the rest.
//
// A search may look past its match to settle the pattern's priorities, as far as the end of the
// string, so each one is charged the rest of the string times the program, times the capture
// groups Go copies at every step.
type matcher struct {
	m    *meter
	p    *compiledRegex
	s    string // the string, WTF-8
	in   string // regexView(s)
	pos  unitPos
	done bool
}

// next returns the next match, or false when there is none.
func (it *matcher) next() (found, bool, error) {
	if it.done {
		return found{}, false, nil
	}
	p, in, pos := it.p, it.in, it.pos
	rest := len(in) - pos.b
	if err := it.m.work((int64(rest) + 2) * int64(p.insts) * int64(p.groups+2)); err != nil {
		return found{}, false, err
	}
	var hay string
	var at func(t int) unitPos
	re := p.next
	switch {
	case pos.b == 0 && !pos.mid:
		re, hay = p.first, in
		at = func(t int) unitPos { return unitPos{b: t} }
	case !pos.mid:
		_, k := utf8.DecodeLastRuneInString(in[:pos.b])
		base := pos.b - k
		hay = in[base:]
		at = func(t int) unitPos { return unitPos{b: base + t} }
	default:
		if err := it.m.alloc(int64(rest) + 2); err != nil {
			return found{}, false, err
		}
		after := pos.b + 4
		hay = "��" + in[after:]
		at = func(t int) unitPos {
			if t <= 3 {
				return unitPos{b: pos.b, mid: true}
			}
			return unitPos{b: after + t - 6}
		}
	}
	if err := it.m.alloc(int64(16 * (p.groups + 2))); err != nil {
		return found{}, false, err
	}
	loc := re.FindStringSubmatchIndex(hay)
	if loc == nil {
		it.done = true
		return found{}, false, nil
	}
	f := found{start: at(loc[2]), end: at(loc[3])}
	if p.groups > 0 {
		f.groups = make([]unitPos, 0, 2*p.groups)
		for i := 4; i < len(loc); i++ {
			if loc[i] < 0 {
				f.groups = append(f.groups, unitPos{b: -1})
			} else {
				f.groups = append(f.groups, at(loc[i]))
			}
		}
	}
	// Where the next search starts: after an empty match, one unit on.
	it.pos = f.end
	if f.start == f.end {
		switch {
		case f.end.mid:
			it.pos = unitPos{b: f.end.b + 4}
		case f.end.b >= len(it.s):
			it.done = true
		default:
			if r, n := decodeWTF8(it.s[f.end.b:]); r >= 0x10000 {
				it.pos = unitPos{b: f.end.b, mid: true}
			} else {
				it.pos = unitPos{b: f.end.b + n}
			}
		}
	}
	return f, true, nil
}

// newMatcher returns a matcher of text in s.
func newMatcher(m *meter, s, text string) (*matcher, error) {
	p, err := m.regexes.get(m, text)
	if err != nil {
		return nil, err
	}
	in, err := viewOf(m, s)
	if err != nil {
		return nil, err
	}
	return &matcher{m: m, p: p, s: s, in: in}, nil
}

// pieceBytes is the size of one piece of split's result before it is built: a string header
// in the list, and the bytes of a half when a piece starts or ends in the middle of a pair.
func pieceBytes(a, b unitPos) int {
	n := b.b - a.b
	if a.mid {
		n--
	}
	if b.mid {
		n += 3
	}
	return n
}

// splitString is s.split(pattern), as RE2J's Pattern.split with no limit (production, the
// review's probes of the Rules Test API, 2026-10-24): an empty piece is held back and kept only
// when a piece that is not empty follows, so the empty pieces at the end are dropped and those
// at the start kept (',,a'.split(',') == [”, ”, 'a'], 'abc'.split(”) == [”, 'a', 'b', 'c']);
// when nothing is left, the result is [”] (','.split(',') == [”]); and with no match, the
// whole string.
func splitString(m *meter, s, text string) (Value, error) {
	it, err := newMatcher(m, s, text)
	if err != nil {
		return Value{}, err
	}
	var parts [][2]unitPos
	keep := func(a, b unitPos) error {
		if err := m.alloc(elemBytes + int64(pieceBytes(a, b))); err != nil {
			return err
		}
		if err := m.value(0, int64(len(parts)+1)); err != nil {
			return err
		}
		parts = append(parts, [2]unitPos{a, b})
		return nil
	}
	last := unitPos{}
	skipped := 0
	for {
		f, ok, err := it.next()
		if err != nil {
			return Value{}, err
		}
		if !ok {
			break
		}
		if last == f.start {
			skipped++
			last = f.end
			continue
		}
		for ; skipped > 0; skipped-- {
			if err := keep(last, last); err != nil {
				return Value{}, err
			}
		}
		if err := keep(last, f.start); err != nil {
			return Value{}, err
		}
		last = f.end
	}
	end := unitPos{b: len(s)}
	if last != end {
		for ; skipped > 0; skipped-- {
			if err := keep(last, last); err != nil {
				return Value{}, err
			}
		}
		if err := keep(last, end); err != nil {
			return Value{}, err
		}
	}
	if len(parts) == 0 {
		if err := keep(last, end); err != nil {
			return Value{}, err
		}
	}
	out := make([]Value, len(parts))
	for i, p := range parts {
		out[i] = String(sliceAt(s, p[0], p[1]))
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
// (Pyric stdlib-string-bytes-hashing: '$1$1' and '\$', production). Production answers an
// invalid replacement with an internal error (HTTP 500, the review's probes), whose text is not
// observable; here it is an evaluation error, a denial.
func parseReplacement(p *compiledRegex, repl string) ([]replacement, error) {
	invalid := errorf("Invalid replacement string: %s.", repl)
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
				g := p.whole.SubexpIndex(repl[i+2 : i+2+end])
				if g < 0 {
					return nil, invalid
				}
				out = append(out, replacement{group: g})
				i += 2 + end
				continue
			}
			d := repl[i+1]
			if d < '0' || d > '9' || int(d-'0') > p.groups {
				return nil, invalid
			}
			g := int(d - '0')
			i++
			for i+1 < len(repl) && repl[i+1] >= '0' && repl[i+1] <= '9' {
				next := g*10 + int(repl[i+1]-'0')
				if next > p.groups {
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

// replaceString is s.replace(pattern, replacement): every match replaced, as RE2J's
// Matcher.replaceAll. The replacement is read only at the first match, as Java does.
func replaceString(m *meter, s, text, repl string) (Value, error) {
	it, err := newMatcher(m, s, text)
	if err != nil {
		return Value{}, err
	}
	var matches []found
	var pieces []replacement
	size := 0
	at := unitPos{}
	for {
		f, ok, err := it.next()
		if err != nil {
			return Value{}, err
		}
		if !ok {
			break
		}
		if matches == nil {
			// Reading the replacement allocates at most a piece for every byte of it.
			if err := m.work(walkWork(len(repl))); err != nil {
				return Value{}, err
			}
			if err := m.alloc(int64(len(repl)) * elemBytes); err != nil {
				return Value{}, err
			}
			if pieces, err = parseReplacement(it.p, repl); err != nil {
				return Value{}, err
			}
		}
		// Applying the pieces to this match: their work, the match kept, the bytes it adds.
		if err := m.work(int64(len(pieces)) + 1); err != nil {
			return Value{}, err
		}
		add := pieceBytes(at, f.start)
		for _, r := range pieces {
			if r.group < 0 {
				add += len(r.text)
			} else {
				a, b := groupAt(f, r.group)
				if a.b >= 0 {
					add += pieceBytes(a, b)
				}
			}
		}
		size += add
		if err := m.value(int64(size), 0); err != nil {
			return Value{}, err
		}
		if err := m.alloc(int64(len(f.groups)+2) * 16); err != nil { // the match, kept
			return Value{}, err
		}
		matches = append(matches, f)
		at = f.end
	}
	if matches == nil {
		return String(s), nil
	}
	size += pieceBytes(at, unitPos{b: len(s)})
	if err := chargeString(m, size); err != nil {
		return Value{}, err
	}
	var w wtf8Builder
	w.grow(size)
	at = unitPos{}
	for _, f := range matches {
		w.add(sliceAt(s, at, f.start))
		for _, r := range pieces {
			if r.group < 0 {
				w.add(r.text)
			} else if a, b := groupAt(f, r.group); a.b >= 0 {
				w.add(sliceAt(s, a, b))
			}
		}
		at = f.end
	}
	w.add(sliceAt(s, at, unitPos{b: len(s)}))
	return String(w.String()), nil
}

// groupAt returns where group g of a match starts and ends; an unset group starts at -1.
func groupAt(f found, g int) (unitPos, unitPos) {
	if g == 0 {
		return f.start, f.end
	}
	return f.groups[2*g-2], f.groups[2*g-1]
}
