package rules

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Production's matcher, RE2J, reads a lone surrogate as its own code point: '😀'[0] matches
// '[\x{D800}-\x{DFFF}]' and '[^\x{FFFD}]', not '\x{FFFD}' (the review's probes of the Rules Test
// API, 2026-10-24). Go's regexp reads only UTF-8, where no surrogate exists, so a string where a
// lone surrogate is read (one it holds, or a half that a search in the middle of a pair starts
// at) is matched in a view where each lone surrogate is a sentinel, against the pattern
// rewritten the same way: a class's surrogates become their sentinels, and what it named of
// the sentinels' block before is dropped.
//
// The block of sentinels is chosen for each string: the first block of 2048 characters, each
// three bytes as a surrogate's own form so offsets agree, that holds no character of the string
// and none that case folding pairs with another (so (?i) cannot reach a sentinel). The private
// use blocks come first. Dropping a block the string does not use changes nothing it can match,
// so the rewritten pattern answers on the string as the pattern would. A string with characters
// in every one of the 30 blocks and a lone surrogate to read leaves none: an evaluation error,
// a denial; only a string written for it holds that.
//
// A string the matcher reads no lone surrogate in never meets a rewritten pattern.

// sentinelBlocks are the bases of the blocks the sentinels may take, in the order chosen.
var sentinelBlocks = func() []rune {
	var out []rune
	usable := func(base rune) bool {
		for r := base; r < base+0x800; r++ {
			if unicode.SimpleFold(r) != r {
				return false
			}
		}
		return true
	}
	for _, base := range []rune{0xE000, 0xE800, 0xF000} {
		out = append(out, base)
	}
	for base := rune(0x800); base < 0x10000; base += 0x800 {
		if base == 0xD800 || slices.Contains(out, base) || !usable(base) {
			continue
		}
		out = append(out, base)
	}
	return out
}()

// errNoSentinels is the error for a string that leaves no block for the sentinels.
func errNoSentinels() *evalError {
	return errorf("A string holding characters from every block the matcher can read lone surrogates through cannot be matched.")
}

// pickBlock returns the base of the first block of sentinels that s holds no character of,
// charging the walk.
func pickBlock(m *meter, s string) (rune, error) {
	if err := m.work(walkWork(len(s))); err != nil {
		return 0, err
	}
	var used [0x10000 >> 11]bool
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		if r < 0x10000 {
			used[r>>11] = true
		}
		i += n
	}
	for _, base := range sentinelBlocks {
		if !used[base>>11] {
			return base, nil
		}
	}
	return 0, errNoSentinels()
}

// sentinel is the sentinel of the surrogate r in the block at base.
func sentinel(base, r rune) rune { return r - 0xD800 + base }

// loneView is the matcher's view of s: each lone surrogate its sentinel in the block at base,
// which s holds no character of.
func loneView(s string, base rune) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		if n == 3 && isSurrogate(r) {
			out = utf8.AppendRune(out, sentinel(base, r))
		} else {
			out = append(out, s[i:i+n]...)
		}
		i += n
	}
	return string(out)
}

// rewriteLone rewrites a parsed pattern for the view with sentinels at base: surrogates become
// their sentinels, and what named the block itself names nothing.
func rewriteLone(re *syntax.Regexp, base rune) {
	last := base + 0x7FF
	switch re.Op {
	case syntax.OpLiteral:
		for i, r := range re.Rune {
			switch {
			case isSurrogate(r):
				re.Rune[i] = sentinel(base, r)
			case r >= base && r <= last:
				*re = syntax.Regexp{Op: syntax.OpNoMatch}
				return
			}
		}
	case syntax.OpCharClass:
		var out []rune
		for i := 0; i+1 < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			// Without the block.
			if lo < base {
				out = append(out, lo, min(hi, base-1))
			}
			if hi > last {
				out = append(out, max(lo, last+1), hi)
			}
			// The surrogates it holds, as sentinels.
			if a, b := max(lo, 0xD800), min(hi, 0xDFFF); a <= b {
				out = append(out, sentinel(base, a), sentinel(base, b))
			}
		}
		pairs := make([][2]rune, 0, len(out)/2)
		for i := 0; i < len(out); i += 2 {
			pairs = append(pairs, [2]rune{out[i], out[i+1]})
		}
		slices.SortFunc(pairs, func(a, b [2]rune) int { return int(a[0] - b[0]) })
		// Sorted and merged where they touch, as syntax keeps a class (String negates by gaps).
		re.Rune = re.Rune[:0]
		for _, p := range pairs {
			if n := len(re.Rune); n > 0 && p[0] <= re.Rune[n-1]+1 {
				re.Rune[n-1] = max(re.Rune[n-1], p[1])
				continue
			}
			re.Rune = append(re.Rune, p[0], p[1])
		}
		if len(re.Rune) == 0 {
			*re = syntax.Regexp{Op: syntax.OpNoMatch}
		}
	}
	for _, sub := range re.Sub {
		rewriteLone(sub, base)
	}
}

// loneCacheBlocks is how many blocks' forms a pattern keeps; the forms for another block are
// compiled for each evaluation that needs them.
const loneCacheBlocks = 4

// loneForms returns the pattern's three forms for the view with sentinels at base, charged to
// the evaluation that compiles them; a form past the limits is an evaluation error. An
// evaluation that runs out of its own limits on the way leaves them for the next to compile.
func (p *compiledRegex) loneForms(m *meter, limit int, base rune) (*regexForms, error) {
	p.loneMu.Lock()
	defer p.loneMu.Unlock()
	if f, ok := p.lone[base]; ok {
		return f, nil
	}
	if p.loneErr != nil {
		return nil, p.loneErr
	}
	if err := m.work(int64(len(p.text))); err != nil {
		return nil, err
	}
	if err := m.alloc(parseCost(p.text)); err != nil {
		return nil, err
	}
	re, err := syntax.Parse(p.text, syntax.Perl)
	if err != nil {
		p.loneErr = errRegex(p.text)
		return nil, p.loneErr
	}
	rewriteLone(re, base)
	text := re.String()
	textCost := parseCost(text)
	if textCost > int64(limit)*parseBytesPerInst {
		p.loneErr = errTooComplex(p.text, -1, limit)
		return nil, p.loneErr
	}
	if err := m.alloc(3*textCost + 3*int64(p.insts+8)*progBytesPerInst); err != nil {
		return nil, err
	}
	if err := m.work(3 * (int64(len(text)) + 4*int64(p.insts))); err != nil {
		return nil, err
	}
	f, ferr := compileForms(text)
	if ferr != nil {
		p.loneErr = ferr
		return nil, p.loneErr
	}
	if len(p.lone) < loneCacheBlocks {
		if p.lone == nil {
			p.lone = map[rune]*regexForms{}
		}
		p.lone[base] = f
	}
	return f, nil
}

// compileForms compiles the three forms of a pattern's text.
func compileForms(text string) (*regexForms, error) {
	var f regexForms
	var err error
	for _, c := range []struct {
		re   **regexp.Regexp
		form string
	}{
		{&f.whole, `^(?:` + text + `)$`},
		{&f.first, `\A(?s:.*?)(` + text + `)`},
		{&f.next, `\A(?s:.)(?s:.*?)(` + text + `)`},
	} {
		if *c.re, err = regexp.Compile(c.form); err != nil {
			return nil, errRegex(text)
		}
	}
	return &f, nil
}
