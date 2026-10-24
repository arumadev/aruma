package rules

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"unicode/utf8"
)

// Production's matcher, RE2J, reads a lone surrogate as its own code point: '😀'[0] matches
// '[\x{D800}-\x{DFFF}]' and '[^\x{FFFD}]', not '\x{FFFD}' (the review's probes of the Rules Test
// API, 2026-10-24). Go's regexp reads only UTF-8, where no surrogate exists, so a string holding
// lone surrogates is matched in a view where each is a sentinel, U+E000 to U+E7FF for U+D800 to
// U+DFFF, three bytes as the surrogate's own form so offsets agree, against the pattern rewritten
// the same way: a class's surrogates become their sentinels, and the sentinels it named before
// are dropped. A string that holds both lone surrogates and real characters of the sentinels'
// range cannot be told apart and is an evaluation error, a denial: private-use characters that
// meet a lone surrogate in one string.

const (
	sentinelBase = 0xE000
	sentinelLast = sentinelBase + 0xDFFF - 0xD800
)

func sentinel(r rune) rune { return r - 0xD800 + sentinelBase }

func inSentinels(r rune) bool { return r >= sentinelBase && r <= sentinelLast }

// errSentinelClash is the error for a string the matcher cannot read.
func errSentinelClash() *evalError {
	return errorf("A string holding both lone surrogates and characters from U+E000 to U+E7FF cannot be matched.")
}

// loneView is the matcher's view of s, which holds lone surrogates: each one its sentinel.
func loneView(s string) (string, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		switch {
		case n == 3 && isSurrogate(r):
			out = utf8.AppendRune(out, sentinel(r))
		case inSentinels(r):
			return "", errSentinelClash()
		default:
			out = append(out, s[i:i+n]...)
		}
		i += n
	}
	return string(out), nil
}

// rewriteLone rewrites a parsed pattern for the lone view: surrogates become their sentinels,
// and what named the sentinels themselves names nothing.
func rewriteLone(re *syntax.Regexp) {
	switch re.Op {
	case syntax.OpLiteral:
		for i, r := range re.Rune {
			switch {
			case isSurrogate(r):
				re.Rune[i] = sentinel(r)
			case inSentinels(r):
				*re = syntax.Regexp{Op: syntax.OpNoMatch}
				return
			}
		}
	case syntax.OpCharClass:
		var out []rune
		for i := 0; i+1 < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			// Without the sentinels' range.
			if lo < sentinelBase {
				out = append(out, lo, min(hi, sentinelBase-1))
			}
			if hi > sentinelLast {
				out = append(out, max(lo, sentinelLast+1), hi)
			}
			// The surrogates it holds, as sentinels.
			if a, b := max(lo, 0xD800), min(hi, 0xDFFF); a <= b {
				out = append(out, sentinel(a), sentinel(b))
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
		rewriteLone(sub)
	}
}

// loneForms compiles the pattern's three forms for the lone view, once per pattern, charged to
// the evaluation that first needs them; a form past the limits is an evaluation error. An
// evaluation that runs out of its own limits on the way leaves them for the next to compile.
func (p *compiledRegex) loneForms(m *meter, limit int) (*regexForms, error) {
	p.loneMu.Lock()
	defer p.loneMu.Unlock()
	if p.lone != nil || p.loneErr != nil {
		return p.lone, p.loneErr
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
	rewriteLone(re)
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
	p.lone = f
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
