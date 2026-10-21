package rules

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Regular expressions in rules are not JavaScript's (firebase.google.com/docs/reference/security/
// database/regex). translateRegex reads Firebase's flavour and writes the same pattern in Go's
// syntax, refusing at load what it cannot carry over. What the emulator 4.11.2 showed
// (2026-10-20), and how this follows it:
//
//   - ^ only as the first character and $ only as the last; anywhere else, unescaped, is an error.
//   - The only flag is i, and it folds Unicode case ('ÉÉ' matches /^é+$/i).
//   - A backslash before any character other than d w s D W S makes it literal: \b is "b",
//     \1 is "1", \n is "n". There are no back-references, word boundaries or escapes for
//     control characters.
//   - At the start of the pattern or of a group, * + ? and { are literal characters. So
//     /(?:a)/ is a group matching "?:a": no lookaround or non-capturing syntax exists.
//   - . matches any character, line breaks included.
//   - \d and \w are ASCII. \s is refused by the emulator ("'whitespacechar' not found") but
//     listed by the reference; the reference wins, with Java's set [ \t\n\x0B\f\r].
//   - Quantifiers may follow each other (/a**/, /a+?/): each applies to what precedes it, so
//     a+? is (a+)? and not a lazy a+.
//   - An empty alternative (/(a|)/) is an error, as in the emulator; an empty group () matches
//     the empty string. The emulator loads /a||b/ yet never matches it; Aruma refuses it.
//   - {n}, {n,} and {n,m} repeat, up to 1000 times, Go's limit.
func translateRegex(lit string) (*regexp.Regexp, error) {
	end := strings.LastIndexByte(lit, '/')
	body, flags := lit[1:end], lit[end+1:]
	if flags != "" && flags != "i" {
		return nil, errors.New("regular expressions do not support flags other than i")
	}
	t := &reTranslator{src: body}
	var out strings.Builder
	if flags == "i" {
		out.WriteString("(?i)")
	}
	if strings.HasPrefix(body, "^") {
		out.WriteString("^")
		t.pos = 1
	}
	anchored := false
	if n := len(body); n > t.pos && body[n-1] == '$' && !escaped(body, n-1) {
		t.src, anchored = body[:n-1], true
	}
	alt, err := t.alternation(true)
	if err != nil {
		return nil, fmt.Errorf("Illegal regular expression, %v", err)
	}
	if t.pos < len(t.src) {
		return nil, fmt.Errorf("Illegal regular expression, unmatched ')' at position %d", t.pos+1)
	}
	out.WriteString(alt)
	if anchored {
		out.WriteString("$")
	}
	re, err := regexp.Compile(out.String())
	if err != nil {
		return nil, fmt.Errorf("Illegal regular expression, %v", err)
	}
	return re, nil
}

// maxMatchCost bounds the work of one matches(): the string's length in bytes times the size
// of the pattern's compiled program, in instructions. Go's regexp runs in time linear in that
// product, whatever the pattern, so a bound on it bounds the time the writer, which every
// client of the database waits on, spends in one call. Unbounded, a 10 MB string (the
// default rtdb.limits.string_bytes) against a large pattern held the writer for 59 seconds,
// and a megabyte against /[a-z]{1000}$/ (1003 instructions) for 35 on an Apple M1.
//
// 2^26 lets a 10 MB string through patterns of up to 6 instructions, such as /^[a-z]+$/ or
// /^\d+$/; 5 MB through /^[^@]+@[^@]+\.[a-z]{2,}$/ (13); 64 KB through /[a-z]{1000}$/. At the
// bound the worst of these took 0.9 s on the M1 (TestMatchesIsBounded). Past it, matches()
// fails, which denies, as any rule failing at run time does.
const maxMatchCost = 1 << 26

// ruleRegex is a regular expression of a rule, with the size of its program.
type ruleRegex struct {
	re   *regexp.Regexp
	size int
}

// compileRuleRegex translates and compiles a regular expression literal (translateRegex).
func compileRuleRegex(lit string) (*ruleRegex, error) {
	re, err := translateRegex(lit)
	if err != nil {
		return nil, err
	}
	return &ruleRegex{re: re, size: programSize(re)}, nil
}

// programSize is the number of instructions of re's program, compiled as regexp compiles it.
func programSize(re *regexp.Regexp) int {
	s, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return maxMatchCost // cannot happen: re compiled from the same text
	}
	prog, err := syntax.Compile(s.Simplify())
	if err != nil {
		return maxMatchCost
	}
	return len(prog.Inst)
}

// match runs the expression on s, or fails when that would cost more than maxMatchCost.
func (r *ruleRegex) match(s string) (bool, error) {
	if int64(len(s))*int64(r.size) > maxMatchCost {
		return false, fail("matches() on a string this long with this pattern exceeds the cost limit")
	}
	return r.re.MatchString(s), nil
}

// escaped reports whether the byte at i is preceded by an odd number of backslashes.
func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

type reTranslator struct {
	src string
	pos int
}

// alternation reads branches separated by |, up to a ) or the end. start reports the start of
// the pattern or of a group, where a leading quantifier character is literal.
func (t *reTranslator) alternation(start bool) (string, error) {
	var branches []string
	for {
		seq, err := t.sequence(start)
		if err != nil {
			return "", err
		}
		branches = append(branches, seq)
		if t.pos < len(t.src) && t.src[t.pos] == '|' {
			t.pos++
			start = false
			continue
		}
		break
	}
	if len(branches) > 1 {
		for _, b := range branches {
			if b == "" {
				return "", fmt.Errorf("expected ')' at position %d", t.pos+1)
			}
		}
	}
	return strings.Join(branches, "|"), nil
}

// sequence reads atoms and their quantifiers up to | or ) or the end.
func (t *reTranslator) sequence(start bool) (string, error) {
	var out strings.Builder
	first := true
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if c == '|' || c == ')' {
			break
		}
		var atom string
		switch {
		case first && start && strings.IndexByte("*+?{", c) >= 0:
			atom = regexp.QuoteMeta(string(c))
			t.pos++
		case strings.IndexByte("*+?", c) >= 0:
			return "", fmt.Errorf("nothing to repeat at position %d", t.pos+1)
		case c == '{':
			return "", fmt.Errorf("nothing to repeat at position %d", t.pos+1)
		default:
			a, err := t.atom()
			if err != nil {
				return "", err
			}
			atom = a
		}
		first = false
		quantified := false
		for {
			q, ok, err := t.quantifier()
			if err != nil {
				return "", err
			}
			if !ok {
				break
			}
			if quantified {
				atom = "(?:" + atom + ")"
			}
			atom += q
			quantified = true
		}
		out.WriteString(atom)
	}
	return out.String(), nil
}

func (t *reTranslator) atom() (string, error) {
	c := t.src[t.pos]
	switch c {
	case '(':
		t.pos++
		inner, err := t.alternation(true)
		if err != nil {
			return "", err
		}
		if t.pos >= len(t.src) || t.src[t.pos] != ')' {
			return "", fmt.Errorf("expected ')' at position %d", t.pos+1)
		}
		t.pos++
		return "(" + inner + ")", nil
	case '[':
		return t.set()
	case '.':
		t.pos++
		return `(?s:.)`, nil
	case '\\':
		return t.escape(false)
	case '^':
		return "", errors.New("unescaped ^, ^ can only appear at the start of regular expressions")
	case '$':
		return "", errors.New("unescaped $, $ can only appear at the end of regular expressions")
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	t.pos += size
	return regexp.QuoteMeta(string(r)), nil
}

// escape reads a backslash and what follows it. inSet selects the form valid inside [...].
func (t *reTranslator) escape(inSet bool) (string, error) {
	t.pos++
	if t.pos >= len(t.src) {
		return "", errors.New("trailing backslash")
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	t.pos += size
	switch r {
	case 'd', 'D', 'w', 'W':
		return `\` + string(r), nil
	case 's':
		if inSet {
			return `\t\n\x0B\f\r `, nil
		}
		return `[\t\n\x0B\f\r ]`, nil
	case 'S':
		if inSet {
			return `\S`, nil
		}
		return `[^\t\n\x0B\f\r ]`, nil
	}
	if inSet {
		return setLiteral(r), nil
	}
	return regexp.QuoteMeta(string(r)), nil
}

// quantifier reads * + ? or {n}, {n,}, {n,m} if one follows.
func (t *reTranslator) quantifier() (string, bool, error) {
	if t.pos >= len(t.src) {
		return "", false, nil
	}
	switch c := t.src[t.pos]; c {
	case '*', '+', '?':
		t.pos++
		return string(c), true, nil
	case '{':
		start := t.pos
		t.pos++
		lo, ok := t.integer()
		if !ok {
			return "", false, fmt.Errorf("integer expected at position %d", t.pos+1)
		}
		hi := lo
		if t.pos < len(t.src) && t.src[t.pos] == ',' {
			t.pos++
			hi = -1
			if n, ok := t.integer(); ok {
				hi = n
			}
		}
		if t.pos >= len(t.src) || t.src[t.pos] != '}' {
			return "", false, fmt.Errorf("expected '}' at position %d", t.pos+1)
		}
		t.pos++
		if lo > 1000 || hi > 1000 {
			return "", false, fmt.Errorf("repetition count above 1000 at position %d", start+1)
		}
		if hi >= 0 && hi < lo {
			return "", false, fmt.Errorf("invalid repetition range at position %d", start+1)
		}
		return t.src[start:t.pos], true, nil
	}
	return "", false, nil
}

func (t *reTranslator) integer() (int, bool) {
	start := t.pos
	for t.pos < len(t.src) && isDigit(t.src[t.pos]) {
		t.pos++
	}
	if start == t.pos || t.pos-start > 6 {
		return 0, false
	}
	n, err := strconv.Atoi(t.src[start:t.pos])
	return n, err == nil
}

// set reads a character class. A ] right after [ or [^ is a literal, as is a - at either end.
func (t *reTranslator) set() (string, error) {
	start := t.pos
	t.pos++ // [
	var out strings.Builder
	out.WriteString("[")
	if t.pos < len(t.src) && t.src[t.pos] == '^' {
		out.WriteString("^")
		t.pos++
	}
	for first := true; ; first = false {
		if t.pos >= len(t.src) {
			return "", fmt.Errorf("missing ']' for the set at position %d", start+1)
		}
		if t.src[t.pos] == ']' && !first {
			t.pos++
			out.WriteString("]")
			return out.String(), nil
		}
		lo, class, err := t.setItem()
		if err != nil {
			return "", err
		}
		if class != "" {
			out.WriteString(class)
			continue
		}
		if t.pos+1 < len(t.src) && t.src[t.pos] == '-' && t.src[t.pos+1] != ']' {
			t.pos++
			hi, class, err := t.setItem()
			if err != nil {
				return "", err
			}
			if class != "" { // [a-\d]: the - is a literal, as in Java
				out.WriteString(setLiteral(lo) + `\-` + class)
				continue
			}
			if hi < lo {
				return "", fmt.Errorf("illegal character range at position %d", t.pos)
			}
			out.WriteString(setLiteral(lo) + "-" + setLiteral(hi))
			continue
		}
		out.WriteString(setLiteral(lo))
	}
}

// setItem reads one character of a set, or a class escape such as \d, returned as class.
func (t *reTranslator) setItem() (rune, string, error) {
	if t.src[t.pos] == '\\' {
		if t.pos+1 >= len(t.src) {
			return 0, "", errors.New("trailing backslash")
		}
		if strings.IndexByte("dDwWsS", t.src[t.pos+1]) >= 0 {
			class, err := t.escape(true)
			return 0, class, err
		}
		t.pos++
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	t.pos += size
	return r, "", nil
}

// setLiteral writes one character for use inside a Go character class.
func setLiteral(r rune) string {
	if strings.ContainsRune(`\]^-[`, r) {
		return `\` + string(r)
	}
	return string(r)
}
