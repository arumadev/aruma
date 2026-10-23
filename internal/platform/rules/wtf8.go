package rules

import (
	"unicode/utf16"
	"unicode/utf8"
)

// Strings in the engine are WTF-8: UTF-8 that may also hold a lone surrogate, in the three-byte
// form UTF-8's generalisation gives it (ED A0..BF 80..BF), while a surrogate pair is always the
// character's ordinary four bytes. Production's strings are Java's, sequences of UTF-16 code
// units: '😀'[0] is the lone high surrogate, not U+FFFD, and '😀'[0] + '😀'[1] == '😀'
// (the review's probes of the Rules Test API, 2026-10-24). With one encoding for each sequence
// of units, byte equality is the equality of strings, and byte order the order of code points.
//
// Values from requests are valid UTF-8, which is WTF-8 already. Lone surrogates arise only in
// the engine: from an index or a slice that cuts a pair, from split() or replace() stepping
// into a pair, and from \u escapes in literals (S1-A writes the same encoding). Joining strings
// (+, join, replace) joins a trailing high surrogate and a leading low one into their pair.
//
// Sizes, indexes and slices count UTF-16 units, and strings order by unit, as Java's
// compareTo does.

// walkWork is the work of decoding n bytes (lengths, offsets and order in UTF-16 units);
// scanWork that of comparing or hashing them, which runs word by word.
func walkWork(n int) int64 { return int64(n)/4 + 1 }
func scanWork(n int) int64 { return int64(n)/64 + 1 }

// isSurrogate reports whether r is a surrogate code point.
func isSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDFFF }

// decodeWTF8 decodes the first character of s: a lone surrogate in its three-byte form as the
// surrogate itself, anything else as utf8.DecodeRuneInString does (a byte that is not UTF-8 is
// U+FFFD, one byte).
func decodeWTF8(s string) (rune, int) {
	if len(s) > 0 && s[0] < utf8.RuneSelf {
		return rune(s[0]), 1
	}
	if len(s) >= 3 && s[0] == 0xED && s[1] >= 0xA0 && s[1] <= 0xBF && s[2] >= 0x80 && s[2] <= 0xBF {
		return 0xD000 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F), 3
	}
	return utf8.DecodeRuneInString(s)
}

// appendWTF8 appends r, a surrogate in its three-byte form.
func appendWTF8(dst []byte, r rune) []byte {
	if isSurrogate(r) {
		return append(dst, 0xE0|byte(r>>12), 0x80|byte(r>>6)&0x3F, 0x80|byte(r)&0x3F)
	}
	return utf8.AppendRune(dst, r)
}

// lone returns the three-byte form of the surrogate u.
func lone(u rune) string { return string(appendWTF8(nil, u)) }

// halves returns the surrogates of a character past U+FFFF.
func halves(r rune) (hi, lo rune) { return utf16.EncodeRune(r) }

// hasLoneSurrogate reports whether s holds a lone surrogate.
func hasLoneSurrogate(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == 0xED && s[i+1] >= 0xA0 {
			return true
		}
	}
	return false
}

// unitIter walks the UTF-16 units of a WTF-8 string.
type unitIter struct {
	s   string
	i   int  // the byte the next character starts at
	low rune // the low surrogate still to come of the last character, or 0
}

func (it *unitIter) next() (rune, bool) {
	if it.low != 0 {
		u := it.low
		it.low = 0
		return u, true
	}
	if it.i >= len(it.s) {
		return 0, false
	}
	r, n := decodeWTF8(it.s[it.i:])
	it.i += n
	if r >= 0x10000 {
		hi, lo := halves(r)
		it.low = lo
		return hi, true
	}
	return r, true
}

// utf16Len returns the length of s in UTF-16 units. The caller charges walkWork(len(s)). It
// reads lead bytes only, as walk does.
func utf16Len(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x80 || c >= 0xC0 && c < 0xF0:
			n++ // ASCII, or the lead of a character of one unit (a lone surrogate among them)
		case c >= 0xF0:
			n += 2 // the lead of a pair
		}
	}
	return n
}

// charUnits returns the bytes and UTF-16 units of the character s starts with, from its lead
// byte: WTF-8 strings, which the engine's are, need no more (a stray continuation byte counts
// as a byte of its own, as utf8 decoding makes it U+FFFD).
func charUnits(s string) (bytes, units int) {
	switch c := s[0]; {
	case c < 0xC0:
		return 1, 1
	case c < 0xE0:
		return min(2, len(s)), 1
	case c < 0xF0:
		return min(3, len(s)), 1
	}
	return min(4, len(s)), 2
}

// unitPos is a position in a WTF-8 string counted in UTF-16 units: a byte offset at a character
// boundary, or, with mid, the middle of the four-byte character starting at it (after its high
// surrogate).
type unitPos struct {
	b   int
	mid bool
}

// unitOffset returns the position of unit u in s, walking no further than it; ok is false when
// s holds fewer than u units. The caller charges walkWork of the bytes up to it.
func unitOffset(s string, u int) (unitPos, bool) {
	n := 0
	for i := 0; i < len(s); {
		if n == u {
			return unitPos{b: i}, true
		}
		if s[i] < utf8.RuneSelf {
			n++
			i++
			continue
		}
		size, units := charUnits(s[i:])
		if units == 2 && n+1 == u {
			return unitPos{b: i, mid: true}, true
		}
		n += units
		i += size
	}
	return unitPos{b: len(s)}, n == u
}

// unitOffsets returns the positions of units u and v of s, u <= v, in one walk; ok is false
// when s holds fewer than v units.
func unitOffsets(s string, u, v int) (unitPos, unitPos, bool) {
	a, ok := unitOffset(s, u)
	if !ok {
		return a, a, false
	}
	// From a on: the units between are counted from there.
	rest := v - u
	from := a
	if a.mid {
		// The low half of the pair at a is one unit.
		if rest == 0 {
			return a, a, true
		}
		rest--
		from = unitPos{b: a.b + 4}
	}
	b, ok := unitOffset(s[from.b:], rest)
	b.b += from.b
	return a, b, ok
}

// sliceAt returns the units of s from a to b, which the caller has checked are in order: the
// halves of a pair either cuts are lone surrogates.
func sliceAt(s string, a, b unitPos) string {
	if a == b {
		return ""
	}
	var out []byte
	from := a.b
	if a.mid {
		r, n := decodeWTF8(s[a.b:])
		_, lo := halves(r)
		out = appendWTF8(out, lo)
		from = a.b + n
	}
	if !b.mid {
		if out == nil {
			return s[from:b.b]
		}
		return string(append(out, s[from:b.b]...))
	}
	out = append(out, s[from:b.b]...)
	r, _ := decodeWTF8(s[b.b:])
	hi, _ := halves(r)
	return string(appendWTF8(out, hi))
}

// joins reports whether a ends with a lone high surrogate and b starts with a lone low one,
// which joining makes one character of four bytes.
func joins(a, b string) bool {
	if len(a) < 3 || len(b) < 3 {
		return false
	}
	hi, hn := decodeWTF8(a[len(a)-3:])
	lo, ln := decodeWTF8(b)
	return hn == 3 && ln == 3 && hi >= 0xD800 && hi <= 0xDBFF && lo >= 0xDC00 && lo <= 0xDFFF
}

// wtf8Builder joins strings, a trailing high surrogate and a leading low one into their pair.
type wtf8Builder struct{ b []byte }

func (w *wtf8Builder) grow(n int) { w.b = make([]byte, 0, n) }

func (w *wtf8Builder) add(s string) {
	if n := len(w.b); n >= 3 && len(s) >= 3 {
		hi, hn := decodeWTF8(string(w.b[n-3:]))
		lo, ln := decodeWTF8(s)
		if hn == 3 && ln == 3 && hi >= 0xD800 && hi <= 0xDBFF && lo >= 0xDC00 && lo <= 0xDFFF {
			w.b = utf8.AppendRune(w.b[:n-3], utf16.DecodeRune(hi, lo))
			s = s[3:]
		}
	}
	w.b = append(w.b, s...)
}

func (w *wtf8Builder) String() string { return string(w.b) }

// joinWTF8 is a + b.
func joinWTF8(a, b string) string {
	var w wtf8Builder
	w.grow(len(a) + len(b))
	w.add(a)
	w.add(b)
	return w.String()
}

// replaceLone returns s with each lone surrogate replaced by with, a three-byte character, or by
// a one-byte one; the caller charges the copy.
func replaceLone(s string, with string) string {
	if !hasLoneSurrogate(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, n := decodeWTF8(s[i:])
		if n == 3 && isSurrogate(r) {
			out = append(out, with...)
		} else {
			out = append(out, s[i:i+n]...)
		}
		i += n
	}
	return string(out)
}

// toUTF8 is the UTF-8 of a string, as Java's String.getBytes(UTF_8) writes it: a lone surrogate
// is '?'. toUtf8() and hashing read strings this way (inferred from Java; not probed).
func toUTF8(s string) string { return replaceLone(s, "?") }

// regexView is s as the pattern matcher reads it: a lone surrogate is U+FFFD, three bytes as the
// surrogate's own form, so offsets in either are offsets in both. RE2J reads a lone surrogate
// as itself; only a pattern that names U+FFFD or a surrogate tells the two apart.
func regexView(s string) string { return replaceLone(s, "�") }
