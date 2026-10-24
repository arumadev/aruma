package rules

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The review of S1-B, round 3 (2026-10-24): the sentinels' block is chosen for each string.

// A search that starts in the middle of a pair reads the rest of the string through the
// rewritten pattern: a real character of a sentinels' block there stays itself (the review's
// round 3 probes of the Rules Test API, 2026-10-24, with X a private-use character).
func TestSentinelsLeaveRealCharacters(t *testing.T) {
	const x = ""
	runValueCases(t, []valueCase{
		{"('😀' + X).replace('X|^', '-')", vCall(vs("😀"+x), "replace", vs(x+"|^"), vs("-")), vs("-😀-"), ""},
		{"('😀' + X).replace('X+|^', '-')", vCall(vs("😀"+x), "replace", vs(x+"+|^"), vs("-")), vs("-😀-"), ""},
		{"('😀' + X).split('[\\x{D800}]|^')", vCall(vs("😀"+x), "split", vs(`[\x{D800}]|^`)), vl(vs(""), vs("😀"+x)), ""},
		{"X far away, an unbounded pattern", vCall(vs("😀"+strings.Repeat("y", 1000)+x), "replace", vs(x+"+|^"), vs("-")),
			vs("-😀" + strings.Repeat("y", 1000) + "-"), ""},
		{"a character in each private block", vCall(vs("😀"), "replace", vs("[-]|^"), vs("-")), vs("-😀---"), ""},
	})
}

// On a string without lone surrogates, the forms rewritten for a block the string does not use
// find exactly what the pattern's own forms find, from every position: lone against plain.
func TestLoneFormsMatchPlainOnPlainStrings(t *testing.T) {
	alphabet := []string{"a", "b", "é", "😀", "", "", "", "", "�", "\n", " ", "K", "K"}
	patterns := []string{"a|^", "[-]", "[^�]", ".", `[\x{D800}-\x{DFFF}]|b`, "+|^", "(?i)k",
		"[^a]+", "�|", "(?m)^.", `\b`, "[-￿]{2}", "x*", "😀|", "(?i)[-k]"}
	rng := rand.New(rand.NewPCG(1, 2))
	searches := 0
	for range 300 {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		s := b.String()
		m := newTestMeter()
		base, err := pickBlock(m, s)
		if err != nil {
			t.Fatal(err)
		}
		for _, pat := range patterns {
			p, err := m.regexes.get(m, pat)
			if err != nil {
				t.Fatal(err)
			}
			lone, err := p.loneForms(m, m.regexes.insts, base)
			if err != nil {
				t.Fatal(err)
			}
			if lone.whole.MatchString(s) != p.whole.MatchString(s) {
				t.Errorf("%q matches %q: lone forms differ", pat, s)
			}
			for i := 0; i <= len(s); i++ {
				if i < len(s) && s[i]&0xC0 == 0x80 {
					continue
				}
				for _, f := range [][2]func(string, int) [][]int{
					{p.first.FindAllStringSubmatchIndex, lone.first.FindAllStringSubmatchIndex},
					{p.next.FindAllStringSubmatchIndex, lone.next.FindAllStringSubmatchIndex},
				} {
					want, got := f[0](s[i:], -1), f[1](s[i:], -1)
					if !slices.EqualFunc(got, want, slices.Equal) {
						t.Errorf("%q in %q from %d: lone forms %v, plain %v", pat, s, i, got, want)
					}
					searches++
				}
			}
		}
	}
	t.Logf("%d searches agree", searches)
	// The blocks: the private use ones first, none with case folding.
	if !slices.Equal(sentinelBlocks[:3], []rune{0xE000, 0xE800, 0xF000}) || slices.Contains(sentinelBlocks, 0xD800) {
		t.Errorf("sentinel blocks %x", sentinelBlocks)
	}
}
