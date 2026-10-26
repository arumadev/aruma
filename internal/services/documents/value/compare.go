package value

import (
	"cmp"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Compare orders two values as production's indexes do, which is the order every query
// returns (values-order.json and values-bson.json: orderBy on one field holding every type, and
// the startAt/endAt class of each value). It returns -1, 0 or +1.
//
// Types order null < MinKey < boolean < number < timestamp < BSON timestamp < string < bytes <
// BSON binary < reference < ObjectId < geopoint < regex < array < vector < map < MaxKey, the BSON
// types being the maps of BSONKind. Within a type:
//
//   - numbers: integers, doubles, int32s and Decimal128s interleave and compare exactly (2^53+1
//     sorts between the doubles 2^53 and 2^53+2; int64 max sorts below the double 2^63; the
//     decimal 0.1 below the double 0.1, whose exact value is 0.1000000000000000055…); NaN, of
//     either kind, sorts first, equal to itself; -0.0 equals 0 and 0.0, and the decimal -0;
//   - timestamps by seconds, then nanoseconds; BSON timestamps by seconds, then increment;
//   - strings, bytes and BSON binary (subtype byte first) by their bytes (UTF-8 order: U+E000 <
//     U+FFFF < U+1F600); ObjectIds by their 12 bytes; regexes by pattern, then options;
//   - references by project, database, then path segment by segment, where a numeric id
//     __id<n>__ sorts before any other id and by n (c/__id5__ < c/__id10__ < c/A < c-b/x);
//   - geopoints by latitude, then longitude;
//   - arrays element by element, then by length;
//   - vectors by length, then element by element, where -0.0 sorts before 0.0 (unlike
//     numbers);
//   - maps entry by entry in key order, key (by bytes) before value, then by length.
//
// An index entry holds at most 1500 bytes of a value, counted as Size counts them, and values
// that agree on those bytes tie (the docs' "only the first 1,500 bytes are considered"). The
// budget is spent in order: a reference's 16 bytes first, one byte for each string, map key,
// path segment and regex part before its characters, then characters, bytes and scalars, which
// are kept whole when they start inside the budget (so the last may overrun it), and vector
// elements, which are kept only when all 8 bytes fit. A value cut short sorts after every value
// it is a prefix of (x×1499 < x×1500 = x×1500+"a"), and two values cut at the same point after
// the same prefix tie. The tr-* pairs of values-order.json and values-bson.json pin each
// boundary; how a regex or binary value longer than the budget is cut is not recorded (here as
// strings and bytes).
func Compare(a, b Value) int {
	c := comparer{ra: IndexedLimit, rb: IndexedLimit}
	return c.value(a, b)
}

// IndexedLimit is the size, as Size counts it, an index entry keeps of a value.
const IndexedLimit = 1500

// comparer walks two values together, each with the index budget it has left. A side whose
// budget runs out is cut: it sorts after anything the other side still has there. When both
// are cut at the same point they tie, and stop records that nothing after counts.
type comparer struct {
	ra, rb int
	stop   bool
}

// The ranks of the type order.
const (
	rankNull = iota
	rankMinKey
	rankBoolean
	rankNumber
	rankTimestamp
	rankBSONTimestamp
	rankString
	rankBytes
	rankBinary
	rankReference
	rankObjectID
	rankGeoPoint
	rankRegex
	rankArray
	rankVector
	rankMap
	rankMaxKey
)

var bsonRanks = [...]int{BSONMinKey: rankMinKey, BSONMaxKey: rankMaxKey, BSONInt32: rankNumber, BSONDecimal128: rankNumber,
	BSONTimestamp: rankBSONTimestamp, BSONBinary: rankBinary, BSONObjectID: rankObjectID, BSONRegex: rankRegex}

// typed is a value with its place in the type order, and its BSON content when it has one.
type typed struct {
	Value
	rank int
	b    bson
}

func classify(v Value) typed {
	t := typed{Value: v}
	switch v.kind {
	case KindNull:
		t.rank = rankNull
	case KindBoolean:
		t.rank = rankBoolean
	case KindInteger, KindDouble:
		t.rank = rankNumber
	case KindTimestamp:
		t.rank = rankTimestamp
	case KindString:
		t.rank = rankString
	case KindBytes:
		t.rank = rankBytes
	case KindReference:
		t.rank = rankReference
	case KindGeoPoint:
		t.rank = rankGeoPoint
	case KindArray:
		t.rank = rankArray
	default:
		if b, ok := bsonOf(v); ok {
			t.rank, t.b = bsonRanks[b.kind], b
		} else if v.IsVector() {
			t.rank = rankVector
		} else {
			t.rank = rankMap
		}
	}
	return t
}

// rank is a value's place in the type order.
func rank(v Value) int { return classify(v).rank }

// cut decides a comparison at a point where a side keeps its next item only if keepA (keepB).
func (c *comparer) cut(keepA, keepB bool) (int, bool) {
	switch {
	case keepA && keepB:
		return 0, false
	case !keepA && !keepB:
		c.stop = true
		return 0, true
	case !keepA:
		return 1, true
	default:
		return -1, true
	}
}

// starts is cut for an item that is kept while any budget is left: the start of a value, a
// string's or key's leading byte, a scalar.
func (c *comparer) starts() (int, bool) { return c.cut(c.ra > 0, c.rb > 0) }

func (c *comparer) spend(a, b int) { c.ra -= a; c.rb -= b }

func (c *comparer) value(a, b Value) int {
	if r, done := c.starts(); done {
		return r
	}
	ta, tb := classify(a), classify(b)
	if ta.rank != tb.rank {
		return cmp.Compare(ta.rank, tb.rank)
	}
	switch ta.rank {
	case rankNull, rankMinKey, rankMaxKey:
		c.spend(1, 1)
		return 0
	case rankBoolean:
		c.spend(1, 1)
		return cmp.Compare(a.i, b.i)
	case rankNumber:
		na, nb := numberOf(ta), numberOf(tb)
		c.spend(na.size(), nb.size())
		return compareNumbers(na, nb)
	case rankTimestamp:
		c.spend(8, 8)
		return cmp.Or(cmp.Compare(a.i, b.i), cmp.Compare(a.n, b.n))
	case rankBSONTimestamp:
		c.spend(8, 8)
		return cmp.Compare(ta.b.ts, tb.b.ts)
	case rankString:
		c.spend(1, 1)
		return c.text(a.s, b.s)
	case rankBytes:
		return c.bytes(a.s, b.s)
	case rankBinary:
		return c.bytes(ta.b.s, tb.b.s)
	case rankReference:
		c.spend(16, 16)
		return c.reference(a.s, b.s)
	case rankObjectID:
		c.spend(12, 12)
		return strings.Compare(ta.b.s, tb.b.s)
	case rankGeoPoint:
		c.spend(16, 16)
		return cmp.Or(cmp.Compare(a.f, b.f), cmp.Compare(a.g, b.g))
	case rankRegex:
		// Pattern then options, each a string: a leading byte, then its characters.
		c.spend(1, 1)
		if r := c.text(ta.b.s, tb.b.s); r != 0 || c.stop {
			return r
		}
		if r, done := c.starts(); done {
			return r
		}
		c.spend(1, 1)
		return c.text(ta.b.o, tb.b.o)
	case rankArray:
		return c.array(a.list, b.list)
	case rankVector:
		return c.vector(a.vectorElements(), b.vectorElements())
	}
	return c.fields(a.m, b.m)
}

// bytes compares two byte strings, each kept as far as its budget goes.
func (c *comparer) bytes(a, b string) int {
	ka, kb := min(len(a), max(c.ra, 0)), min(len(b), max(c.rb, 0))
	c.spend(ka, kb)
	return c.prefixes(a[:ka], ka < len(a), b[:kb], kb < len(b))
}

// text compares two strings once their leading byte is spent.
func (c *comparer) text(a, b string) int {
	ka, cutA := keptText(a, c.ra)
	kb, cutB := keptText(b, c.rb)
	c.spend(ka, kb)
	return c.prefixes(a[:ka], cutA, b[:kb], cutB)
}

// keptText is how many bytes of s an index entry keeps with r bytes of budget left: whole
// characters, each kept when it starts inside the budget. cut reports that some were left out.
// x×1498+"é" (1500 bytes) is kept whole; x×1498+"éa" keeps x×1498+"é" (values-order.json,
// s-t-f and s-t-g). Invalid UTF-8, which the wire refuses, counts a stray byte as a character.
func keptText(s string, r int) (n int, cut bool) {
	if r >= len(s) {
		return len(s), false
	}
	if r <= 0 {
		return 0, len(s) > 0
	}
	n = r
	for j := 0; j < utf8.UTFMax-1 && n < len(s) && !utf8.RuneStart(s[n]); j++ {
		n++
	}
	return n, n < len(s)
}

// prefixes compares two kept prefixes, each followed by the cut mark (after everything) when
// cut, or by its end (before everything) when not.
func (c *comparer) prefixes(a string, cutA bool, b string, cutB bool) int {
	n := min(len(a), len(b))
	if r := strings.Compare(a[:n], b[:n]); r != 0 {
		return r
	}
	switch {
	case len(a) < len(b):
		if cutA {
			return 1
		}
		return -1
	case len(a) > len(b):
		if cutB {
			return -1
		}
		return 1
	}
	r, _ := c.cut(!cutA, !cutB)
	return r
}

// ends compares two sequences where at least one has ended: an end sorts before anything,
// including a cut.
func ends(endA, endB bool) int {
	switch {
	case endA && endB:
		return 0
	case endA:
		return -1
	}
	return 1
}

func (c *comparer) array(a, b []Value) int {
	for i := 0; ; i++ {
		if i == len(a) || i == len(b) {
			return ends(i == len(a), i == len(b))
		}
		if r := c.value(a[i], b[i]); r != 0 || c.stop {
			return r
		}
	}
}

func (c *comparer) fields(a, b []Field) int {
	for i := 0; ; i++ {
		if i == len(a) || i == len(b) {
			return ends(i == len(a), i == len(b))
		}
		if r, done := c.starts(); done {
			return r
		}
		c.spend(1, 1)
		if r := c.text(a[i].Name, b[i].Name); r != 0 || c.stop {
			return r
		}
		if r := c.value(a[i].Value, b[i].Value); r != 0 || c.stop {
			return r
		}
	}
}

// vector compares two vectors: the length costs nothing and is always kept once the vector
// starts; an element is kept only when its 8 bytes fit (tr-vector-in-array: [x×1491, [1]]
// fills 1500 exactly and keeps the element, [x×1492, [1]] does not).
func (c *comparer) vector(a, b []Value) int {
	if r := cmp.Compare(len(a), len(b)); r != 0 {
		return r
	}
	for i := range a {
		if r, done := c.cut(c.ra >= 8, c.rb >= 8); done {
			return r
		}
		c.spend(8, 8)
		if r := cmp.Compare(vectorBits(a[i].f), vectorBits(b[i].f)); r != 0 {
			return r
		}
	}
	return 0
}

// vectorBits maps a double to an unsigned integer in the order production gives vector
// elements: the IEEE total order, where -0.0 sorts before 0.0 (values-order.json, v-neg-zero
// and v-zero do not tie). Production refuses NaN in a vector.
func vectorBits(f float64) uint64 {
	b := math.Float64bits(f)
	if b>>63 != 0 {
		return ^b
	}
	return b | 1<<63
}

// reference compares two references once their 16 bytes are spent.
func (c *comparer) reference(a, b string) int {
	pa, da, sa := splitName(a)
	pb, db, sb := splitName(b)
	if r := cmp.Or(strings.Compare(pa, pb), strings.Compare(da, db)); r != 0 {
		return r
	}
	for i := 0; ; i++ {
		if i == len(sa) || i == len(sb) {
			return ends(i == len(sa), i == len(sb))
		}
		if r, done := c.starts(); done {
			return r
		}
		c.spend(1, 1)
		if r := c.segment(sa[i], sb[i]); r != 0 || c.stop {
			return r
		}
	}
}

// segment compares two path segments once their leading byte is spent. A numeric id is kept
// whole, like a scalar.
func (c *comparer) segment(a, b string) int {
	na, numA := numericID(a)
	nb, numB := numericID(b)
	switch {
	case numA && numB:
		if r, done := c.starts(); done {
			return r
		}
		c.spend(len(a), len(b))
		return cmp.Compare(na, nb)
	case numA:
		return -1
	case numB:
		return 1
	}
	return c.text(a, b)
}

// numericID parses a numeric document id, __id<n>__, which Datastore imports produce and which
// sorts before every other id, by n. Only the canonical decimal form counts (no sign but "-",
// no leading zero, no "-0"), so that equal numbers are equal ids.
func numericID(s string) (int64, bool) {
	digits, ok := strings.CutPrefix(s, "__id")
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, "__"); !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != digits {
		return 0, false
	}
	return n, true
}

// splitName splits a document's resource name into its project, database and path segments.
// A name of any other shape is one segment with an empty project and database, which no valid
// name has.
func splitName(name string) (project, database string, path []string) {
	rest, ok := strings.CutPrefix(name, "projects/")
	if ok {
		var ok2, ok3 bool
		project, rest, ok2 = strings.Cut(rest, "/databases/")
		database, rest, ok3 = strings.Cut(rest, "/documents/")
		if ok2 && ok3 && project != "" && database != "" && !strings.Contains(project, "/") && !strings.Contains(database, "/") {
			return project, database, strings.Split(rest, "/")
		}
	}
	return "", "", []string{name}
}

// number is a numeric value: an integer (an int32 too), a double or a Decimal128.
type number struct {
	kind  BSONKind // NotBSON for an integer or a double, else BSONInt32 or BSONDecimal128
	isInt bool
	i     int64
	f     float64
	d     decimal
}

func numberOf(t typed) number {
	switch {
	case t.b.kind == BSONInt32:
		return number{kind: BSONInt32, isInt: true, i: t.b.i}
	case t.b.kind == BSONDecimal128:
		return number{kind: BSONDecimal128, d: t.b.dec}
	case t.kind == KindInteger:
		return number{isInt: true, i: t.i}
	}
	return number{f: t.f}
}

// size is what a number spends of the index budget, as Size counts it.
func (n number) size() int {
	switch n.kind {
	case BSONInt32:
		return 4
	case BSONDecimal128:
		return 16
	}
	return 8
}

// The number classes, in order; each is also the first byte of a number's key.
const (
	classNaN      = 0x01
	classNegInf   = 0x02
	classNegative = 0x03
	classZero     = 0x04
	classPositive = 0x05
	classPosInf   = 0x06
)

func (n number) class() byte {
	switch {
	case n.kind == BSONDecimal128:
		switch {
		case n.d.form == decNaN:
			return classNaN
		case n.d.form == decInf && n.d.neg:
			return classNegInf
		case n.d.form == decInf:
			return classPosInf
		case n.d.coef.Sign() == 0:
			return classZero
		case n.d.neg:
			return classNegative
		}
		return classPositive
	case n.isInt:
		switch {
		case n.i < 0:
			return classNegative
		case n.i == 0:
			return classZero
		}
		return classPositive
	}
	switch f := n.f; {
	case math.IsNaN(f):
		return classNaN
	case math.IsInf(f, -1):
		return classNegInf
	case math.IsInf(f, 1):
		return classPosInf
	case f < 0:
		return classNegative
	case f == 0:
		return classZero
	}
	return classPositive
}

// rat returns a finite number's exact value.
func (n number) rat() *big.Rat {
	switch {
	case n.kind == BSONDecimal128:
		return n.d.rat()
	case n.isInt:
		return new(big.Rat).SetInt64(n.i)
	}
	return new(big.Rat).SetFloat64(n.f)
}

// compareNumbers compares two numbers exactly: by class (NaN, -Inf, negative, zero, positive,
// +Inf), then by value. Integers and doubles compare without big numbers; a Decimal128 compares
// as an exact rational.
func compareNumbers(a, b number) int {
	ca, cb := a.class(), b.class()
	if ca != cb {
		return cmp.Compare(ca, cb)
	}
	if ca != classNegative && ca != classPositive {
		return 0
	}
	switch {
	case a.kind == BSONDecimal128 || b.kind == BSONDecimal128:
		return a.rat().Cmp(b.rat())
	case a.isInt && b.isInt:
		return cmp.Compare(a.i, b.i)
	case !a.isInt && !b.isInt:
		return cmp.Compare(a.f, b.f)
	case !a.isInt:
		return compareDoubleInt(a.f, b.i)
	}
	return -compareDoubleInt(b.f, a.i)
}

// compareDoubleInt compares a double with an integer without rounding either.
func compareDoubleInt(f float64, i int64) int {
	switch {
	case math.IsNaN(f) || f < -0x1p63:
		return -1
	case f >= 0x1p63:
		return 1
	}
	t := math.Trunc(f)
	if r := cmp.Compare(int64(t), i); r != 0 {
		return r
	}
	return cmp.Compare(f-t, 0)
}
