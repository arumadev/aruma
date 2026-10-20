package core

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Firebase's orders, as the JS SDK computes them (firebase-js-sdk d7bb0e16f2,
// packages/database/src/core): keys by nameCompare (util/util.ts:254-277), values by
// LeafNode.compareTo and ChildrenNode.compareTo, children by an index (snap/indexes). The
// server must order exactly as the SDKs do: a client re-applies a query's window to what it
// receives, and drops what it would not have selected itself.

// CompareKeys orders two keys: keys that read as 32-bit integers come first, numerically (a
// tie, such as "1" and "01", goes to the shorter), then the others by UTF-16 code units, as
// JavaScript compares strings. The SDK leaves equal numbers of equal length, such as "00"
// and "-0", tied; the server breaks that tie by text to keep one order.
func CompareKeys(a, b string) int {
	if a == b {
		return 0
	}
	ai, aInt := keyInt(a)
	bi, bInt := keyInt(b)
	switch {
	case aInt && bInt:
		if ai != bi {
			return cmp.Compare(ai, bi)
		}
		if len(a) != len(b) {
			return cmp.Compare(len(a), len(b))
		}
		return strings.Compare(a, b)
	case aInt:
		return -1
	case bInt:
		return 1
	}
	return compareUTF16(a, b)
}

// keyInt parses a key that matches ^-?(0*)\d{1,10}$ and lies within int32 (tryParseInt).
func keyInt(k string) (int64, bool) {
	s := k
	if len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	i := 0
	for i < len(s) && s[i] == '0' {
		i++
	}
	digits := s[i:]
	if i > 0 && digits == "" {
		// Only zeros: \d{1,10} takes the last one.
		digits = "0"
	}
	if len(digits) == 0 || len(digits) > 10 {
		return 0, false
	}
	for j := 0; j < len(digits); j++ {
		if digits[j] < '0' || digits[j] > '9' {
			return 0, false
		}
	}
	for j := 0; j < i; j++ {
		if s[j] != '0' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	if k[0] == '-' {
		v = -v
	}
	if v < -2147483648 || v > 2147483647 {
		return 0, false
	}
	return v, true
}

// compareUTF16 compares as JavaScript's < does: by UTF-16 code units. It differs from Go's
// byte order only when a character above U+FFFF meets one in U+E000-U+FFFF.
func compareUTF16(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			if ua, ub := utf16Unit(ra), utf16Unit(rb); ua != ub {
				return cmp.Compare(ua, ub)
			}
			// Two characters above U+FFFF with the same high surrogate: the low ones
			// order as the code points do.
			return cmp.Compare(ra, rb)
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
}

// utf16Unit is the first UTF-16 code unit of r.
func utf16Unit(r rune) rune {
	if r > 0xFFFF {
		return 0xD800 + (r-0x10000)>>10
	}
	return r
}

// CompareValues orders two nodes as values: null, then false, true, numbers, strings, then
// objects, which all tie (LeafNode.VALUE_TYPE_ORDER and the compareTo methods). Priorities
// play no part.
func CompareValues(a, b *Node) int {
	ra, rb := valueRank(a), valueRank(b)
	if ra != rb {
		return cmp.Compare(ra, rb)
	}
	switch a.Kind() {
	case KindNumber:
		return cmp.Compare(a.n, b.n)
	case KindString:
		return compareUTF16(a.s, b.s)
	}
	return 0
}

func valueRank(n *Node) int {
	switch n.Kind() {
	case KindNull:
		return 0
	case KindBool:
		if n.b {
			return 2
		}
		return 1
	case KindNumber:
		return 3
	case KindString:
		return 4
	case kindServerValue:
		// Deferred values sort first among leaves in the SDK; the server resolves them before
		// anything is ordered.
		return 1
	}
	return 5
}

// Index is what a query orders children by.
type Index struct {
	kind  indexKind
	child Path // ByChild
}

type indexKind uint8

const (
	byPriority indexKind = iota
	byKey
	byValue
	byChild
)

// Indexes.
var (
	ByPriority = Index{kind: byPriority}
	ByKey      = Index{kind: byKey}
	ByValue    = Index{kind: byValue}
)

// ByChild orders by the value at a child path, missing values first as null (PathIndex).
func ByChild(p Path) Index { return Index{kind: byChild, child: p} }

// Compare orders two named children under the index, ties broken by key.
func (ix Index) Compare(aKey string, a *Node, bKey string, b *Node) int {
	var c int
	switch ix.kind {
	case byKey:
		return CompareKeys(aKey, bKey)
	case byPriority:
		c = comparePriorities(a.Priority(), b.Priority())
	case byValue:
		c = CompareValues(a, b)
	case byChild:
		c = CompareValues(a.Get(ix.child), b.Get(ix.child))
	}
	if c != 0 {
		return c
	}
	return CompareKeys(aKey, bKey)
}

// comparePriorities orders priorities: none first, then numbers, then strings.
func comparePriorities(a, b *Node) int { return CompareValues(a, b) }

// SortedKeys returns n's children in index order.
func (n *Node) SortedKeys(ix Index) []string {
	keys := n.Keys()
	if ix.kind == byKey {
		return keys
	}
	slices.SortStableFunc(keys, func(a, b string) int {
		return ix.Compare(a, n.children[a], b, n.children[b])
	})
	return keys
}
