package value

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gen builds values from a byte stream (the fuzzer's) or, when that runs out, from a seeded
// generator. Its alphabets are small so that independent values often share a prefix, tie, or
// meet the index budget at the same point: long strings, ids and vectors sit around 1500 bytes.
type gen struct {
	data []byte
	r    *rand.Rand
}

func (g *gen) n(k int) int {
	if len(g.data) > 0 {
		b := g.data[0]
		g.data = g.data[1:]
		return int(b) % k
	}
	return g.r.IntN(k)
}

var (
	edgeInts    = []int64{0, 1, -1, 2, math.MaxInt64, math.MinInt64, math.MaxInt64 - 1, 1 << 53, 1<<53 + 1, -(1 << 53) - 1}
	edgeDoubles = []float64{0, math.Copysign(0, -1), 1, -1, 1.5, -1.5, 0.5, math.NaN(), math.Inf(1), math.Inf(-1),
		1 << 53, 1<<53 + 2, 0x1p63, -0x1p63, 0x1p63 - 1024, 5e-324, -5e-324, math.MaxFloat64, -math.MaxFloat64, 1e300}
	textBits = []string{"", "a", "b", "x", "é", "ê", "\u0000", "￿", "", "😀", "A"}
	segBits  = []string{"c", "a", "b", "c-b", "__id5__", "__id-1__", "__id10__", "__id-9223372036854775808__", "A", "é"}
)

func (g *gen) text() string {
	switch g.n(4) {
	case 0: // around the string budget
		return strings.Repeat("x", 1490+g.n(14)) + textBits[g.n(len(textBits))]
	case 1:
		return strings.Repeat("x", 490+g.n(14)) + textBits[g.n(len(textBits))]
	}
	var b strings.Builder
	for range g.n(4) {
		b.WriteString(textBits[g.n(len(textBits))])
	}
	return b.String()
}

// edgeDecimals are Decimal128 strings around the doubles and integers above, and beyond them.
var edgeDecimals = []string{"0", "-0", "0.1", "-0.1", "0.1000000000000000055511151231257827", "0.1000000000000000055511151231257828",
	"1", "1.0", "1.5", "-1.5", "2.5", "9007199254740993", "9223372036854775807", "9223372036854775808", "-9223372036854775809",
	"1E+308", "1E+309", "-1E+309", "1E-400", "1E-6176", "9.999999999999999999999999999999999E+6144", "NaN", "Infinity",
	"-Infinity", "1234567890123456789012345678901234", "4.9E-324", "18446744073709551617", "36893488147419103233"}

// bson returns a BSON shape: one of each kind, with contents that tie, nearly tie or sit near
// the index budget.
func (g *gen) bson() Value {
	switch g.n(8) {
	case 0:
		return MinKey()
	case 1:
		return MaxKey()
	case 2:
		return Int32(int32(g.n(5)) - 2)
	case 3:
		d, ok := Decimal128(edgeDecimals[g.n(len(edgeDecimals))])
		if !ok {
			panic("bad edge decimal")
		}
		return d
	case 4:
		return BSONTimestampOf(uint32(g.n(3))*(1<<31), uint32(g.n(3)))
	case 5:
		return BSONBinaryOf(byte(1+g.n(255)), []byte(g.text()))
	case 6:
		var id [12]byte
		id[g.n(12)] = byte(g.n(256))
		return ObjectID(id)
	}
	return Regex(g.text(), []string{"", "i", "imsux", "m"}[g.n(4)])
}

func (g *gen) value(depth int) Value {
	k := g.n(14)
	if depth > 3 && k >= 9 && k != 13 {
		k = g.n(9)
	}
	switch k {
	case 0:
		return Null()
	case 1:
		return Bool(g.n(2) == 1)
	case 2:
		if g.n(3) == 0 {
			return Int(int64(g.n(5)) - 2)
		}
		return Int(edgeInts[g.n(len(edgeInts))])
	case 3:
		return Double(edgeDoubles[g.n(len(edgeDoubles))])
	case 4:
		return Timestamp(int64(g.n(3))-1, int32(g.n(3))*1000)
	case 5:
		return String(g.text())
	case 6:
		b := []byte(g.text())
		if g.n(2) == 0 {
			b = append(b, 0xFF, 0x00)
		}
		return Bytes(b)
	case 7:
		segs := []string{"c", segBits[g.n(len(segBits))]}
		if g.n(3) == 0 {
			segs[1] = strings.Repeat("i", 1470+g.n(20))
		}
		for range g.n(2) {
			segs = append(segs, segBits[g.n(len(segBits))], segBits[g.n(len(segBits))])
		}
		db := []string{"(default)", "other"}[g.n(2)]
		return Reference("projects/" + []string{"p", "q"}[g.n(2)] + "/databases/" + db + "/documents/" + strings.Join(segs, "/"))
	case 8:
		return GeoPoint(float64(g.n(3)-1), edgeDoubles[g.n(6)])
	case 9:
		els := make([]Value, g.n(4))
		for i := range els {
			els[i] = g.value(depth + 1)
		}
		return Array(els...)
	case 13:
		return g.bson()
	case 10:
		ds := make([]float64, g.n(4))
		if g.n(4) == 0 {
			ds = make([]float64, 180+g.n(12))
		}
		for i := range ds {
			ds[i] = []float64{0, math.Copysign(0, -1), 1, 2, -1.5}[g.n(5)]
		}
		return Vector(ds...)
	}
	fs := make([]Field, g.n(4))
	for i := range fs {
		fs[i] = Field{g.text(), g.value(depth + 1)}
		if len(fs[i].Name) > 600 {
			fs[i].Name = fs[i].Name[:g.n(3)]
		}
	}
	return Map(fs...)
}

// mutate returns a value near v: the same, its numeric twin, or one with its tail changed, so
// that pairs often tie or differ only past the index budget.
func (g *gen) mutate(v Value) Value {
	switch g.n(4) {
	case 0:
		return v
	case 1:
		switch v.kind {
		case KindInteger:
			if g.n(2) == 0 && v.i >= math.MinInt32 && v.i <= math.MaxInt32 {
				return Int32(int32(v.i))
			}
			if d, ok := Decimal128(strconv.FormatInt(v.i, 10)); ok && g.n(2) == 0 {
				return d
			}
			return Double(float64(v.i))
		case KindDouble:
			if g.n(2) == 0 && !math.IsNaN(v.f) && !math.IsInf(v.f, 0) {
				// The decimal of the shortest string that reads back as v, which is v's
				// neighbour or v itself.
				if d, ok := Decimal128(strconv.FormatFloat(v.f, 'g', -1, 64)); ok {
					return d
				}
			}
			if v.f == math.Trunc(v.f) && math.Abs(v.f) < 1<<62 {
				return Int(int64(v.f))
			}
		case KindString:
			return String(v.s + textBits[g.n(len(textBits))])
		case KindArray:
			if len(v.list) > 0 {
				els := slices.Clone(v.list)
				els[len(els)-1] = g.mutate(els[len(els)-1])
				return Array(els...)
			}
		}
	}
	return g.value(0)
}

// checkPair checks Key against Compare and the order's laws on a, b and c.
func checkPair(t *testing.T, a, b, c Value) {
	t.Helper()
	ka, kb := Key(a), Key(b)
	ab := Compare(a, b)
	if got := bytes.Compare(ka, kb); got != ab {
		t.Fatalf("Compare(%v, %v) = %d, keys compare %d", a, b, ab, got)
	}
	if ba := Compare(b, a); ba != -ab {
		t.Fatalf("Compare(%v, %v) = %d but Compare(b, a) = %d", a, b, ab, ba)
	}
	if Compare(a, a) != 0 {
		t.Fatalf("Compare(%v, itself) != 0", a)
	}
	bc, ac := Compare(b, c), Compare(a, c)
	if ab <= 0 && bc <= 0 && ac > 0 || ab >= 0 && bc >= 0 && ac < 0 || ab == 0 && bc == 0 && ac != 0 {
		t.Fatalf("not transitive: a=%v b=%v c=%v: %d %d %d", a, b, c, ab, bc, ac)
	}
	if eq := Equal(a, b); eq != (ab == 0 && !a.IsNull() && !a.IsNaN() && !b.IsNull() && !b.IsNaN()) {
		t.Fatalf("Equal(%v, %v) = %v with Compare %d", a, b, eq, ab)
	}
	// A value is cut only where the budget runs out, so at least 1500 bytes come before the
	// cut; [x×1499, []] is cut at exactly 1500, its empty array having no budget left.
	if IndexSize(a) < IndexedLimit && Truncated(a) {
		t.Fatalf("%v is cut but its IndexSize is %d", a, IndexSize(a))
	}
	// A value lies inside the key bounds of its own class (but NaN), and outside every other's.
	if lo, hi, ok := ClassBounds(b); ok {
		inside := bytes.Compare(ka, lo) >= 0 && bytes.Compare(ka, hi) < 0
		if want := SameClass(a, b) && !a.IsNaN(); inside != want {
			t.Fatalf("Key(%v) inside the class bounds of %v: %v, want %v", a, b, inside, want)
		}
	}
	d, err := DecodeKey(ka)
	switch {
	case Truncated(a):
		if !errors.Is(err, ErrCutKey) {
			t.Fatalf("DecodeKey of the cut %v: %v, %v", a, d, err)
		}
	case err != nil:
		t.Fatalf("DecodeKey(Key(%v)): %v", a, err)
	case Compare(d, a) != 0 || !bytes.Equal(Key(d), ka):
		t.Fatalf("DecodeKey(Key(%v)) = %v", a, d)
	}
}

// FuzzKey checks that sort keys order as Compare does, that Compare is a total order, and that
// keys decode back. go test -fuzz=FuzzKey -fuzztime=60s ./internal/services/documents/value
func FuzzKey(f *testing.F) {
	for _, seed := range []string{"", "\x05\x03", "\x09\x02\x05\x00\x05\x01", "\x0c\x03\x00\x02", "\x07\x00\x01", "\x0a\x03"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &gen{data: data, r: rand.New(rand.NewPCG(uint64(len(data)), 1))}
		a := g.value(0)
		b := g.mutate(a)
		c := g.mutate(b)
		checkPair(t, a, b, c)
		checkPair(t, b, c, a)
	})
}

// FuzzDecodeKey feeds arbitrary bytes to DecodeKey: no panic, and what it accepts is a key.
func FuzzDecodeKey(f *testing.F) {
	dec, _ := Decimal128("-0.1")
	for _, v := range []Value{Null(), Int(7), Double(-1.5), String("a\x00\xff"), Vector(1, 2), Reference("projects/p/databases/d/documents/c/__id5__"),
		Map(Field{"a", Array(Bool(true), Timestamp(1, 2))}), GeoPoint(1, 2), dec, MinKey(), Regex("a", "i"),
		BSONTimestampOf(1, 2), BSONBinaryOf(4, []byte{1}), ObjectID([12]byte{1}), Array(MaxKey(), Int32(-3))} {
		f.Add(Key(v))
	}
	f.Fuzz(func(t *testing.T, k []byte) {
		v, err := DecodeKey(k)
		if err == nil && !bytes.Equal(Key(v), k) {
			t.Fatalf("DecodeKey(%x) = %v, whose key is %x", k, v, Key(v))
		}
	})
}

// TestCompareIsATotalOrder runs the fuzz checks over random triples and sorts a random list,
// with a seed printed on failure.
func TestCompareIsATotalOrder(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	g := &gen{r: rand.New(rand.NewPCG(seed, 2))}
	for range 3000 {
		a := g.value(0)
		b := g.mutate(a)
		checkPair(t, a, b, g.mutate(b))
	}
	vs := make([]Value, 400)
	for i := range vs {
		vs[i] = g.mutate(g.value(0))
	}
	slices.SortFunc(vs, Compare)
	for i := 1; i < len(vs); i++ {
		if bytes.Compare(Key(vs[i-1]), Key(vs[i])) > 0 {
			t.Fatalf("keys out of order after sorting: %v then %v", vs[i-1], vs[i])
		}
	}
}

func TestCompareNumbers(t *testing.T) {
	negZero := math.Copysign(0, -1)
	// Ascending, each line one class of equal values (values-order.json).
	dec := func(s string) Value {
		d, ok := Decimal128(s)
		if !ok {
			t.Fatalf("Decimal128(%q) refused", s)
		}
		return d
	}
	order := [][]Value{
		{Double(math.NaN()), dec("NaN"), dec("-NaN")},
		{Double(math.Inf(-1)), dec("-Infinity")},
		{dec("-1E+309")},
		{Double(-0x1p63 - 2048)},
		{Int(math.MinInt64), Double(-0x1p63)},
		{Int(math.MinInt64 + 1)},
		{Int(-(1 << 53) - 1)},
		{Double(-(1 << 53))},
		{Int(-1), Double(-1), Int32(-1), dec("-1.000")},
		{Double(-0.1)},
		{dec("-0.1")},
		{Double(-5e-324)},
		{Int(0), Double(0), Double(negZero), Int32(0), dec("0"), dec("-0"), dec("0E+10"), dec("0E-6176")},
		{dec("1E-6176")},
		{Double(5e-324)},
		{dec("0.1")},
		{dec("0.1000000000000000055511151231257827")},
		{Double(0.1)},
		{dec("0.1000000000000000055511151231257828")},
		{Int(1), Double(1), Int32(1), dec("1"), dec("1.0"), dec("1E0")},
		{Int(1 << 53), Double(1 << 53), dec("9007199254740992")},
		{Int(1<<53 + 1), dec("9007199254740993")},
		{Int(1<<53 + 2), Double(1<<53 + 2)},
		{Double(0x1p63 - 1024)},
		{Int(math.MaxInt64 - 1)},
		{Int(math.MaxInt64), dec("9223372036854775807")},
		{dec("9223372036854775807.5")},
		{Double(0x1p63), dec("9223372036854775808")},
		{dec("18446744073709551617")},
		{dec("1E+308")},
		{Double(1e308)},
		{Double(math.MaxFloat64)},
		{dec("1E+309")},
		{dec("9.999999999999999999999999999999999E+6144")},
		{Double(math.Inf(1)), dec("Infinity"), dec("inf")},
	}
	for i, class := range order {
		for j, other := range order {
			for _, a := range class {
				for _, b := range other {
					if got := Compare(a, b); got != cmpInt(i, j) {
						t.Errorf("Compare(%v, %v) = %d, want %d", a, b, got, cmpInt(i, j))
					}
					if got := bytes.Compare(Key(a), Key(b)); got != cmpInt(i, j) {
						t.Errorf("keys of %v and %v compare %d, want %d", a, b, got, cmpInt(i, j))
					}
				}
			}
		}
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestCompareSpecifics(t *testing.T) {
	ref := func(path string) Value { return Reference("projects/p/databases/(default)/documents/" + path) }
	ascending := []Value{
		Null(), Bool(false), Bool(true), Int(1), Timestamp(0, 0), String(""), String(""), String("￿"), String("😀"),
		Bytes(nil), Bytes([]byte{0xFF}),
		Reference("projects/a/databases/(default)/documents/c/a"),
		ref("c/__id-1__"), ref("c/__id5__"), ref("c/__id10__"), ref("c/A"), ref("c/a"), ref("c/a/s/x"), ref("c/b"), ref("c-b/x"),
		Reference("projects/p/databases/other/documents/c/a"),
		GeoPoint(-90, 180), GeoPoint(0, 0), GeoPoint(0, 1),
		Array(), Array(Null()), Array(Int(1)), Array(Int(1), Int(2)), Array(Int(2)),
		Vector(math.Copysign(0, -1)), Vector(0), Vector(2), Vector(-1, 5), Vector(1, 2), Vector(0, 0, 0),
		Map(), Map(Field{"A", Int(1)}), Map(Field{"a", Int(1)}), Map(Field{"a", Int(1)}, Field{"b", Int(1)}), Map(Field{"a", Int(2)}), Map(Field{"b", Int(0)}),
	}
	for i := 1; i < len(ascending); i++ {
		if Compare(ascending[i-1], ascending[i]) >= 0 {
			t.Errorf("%v should sort before %v", ascending[i-1], ascending[i])
		}
	}
	x := func(n int, tail string) Value { return String(strings.Repeat("x", n) + tail) }
	for _, c := range []struct {
		a, b Value
		want int
	}{
		{x(1499, ""), x(1500, ""), -1},    // cut sorts after its prefix
		{x(1500, ""), x(1500, "a"), 0},    // both cut after 1499 bytes
		{x(1499, "é"), x(1500, ""), 0},    // cut at a character boundary
		{x(1498, "é"), x(1498, "éa"), -1}, // é overruns the budget and is kept whole
		{x(1498, "ya"), x(1498, "yb"), 0}, // the 1500th byte is cut
		{x(1497, "y"), x(1497, "😀"), -1},  // plain byte order inside the budget
		{Array(x(1000, ""), x(497, "a")), Array(x(1000, ""), x(497, "b")), -1},
		{Array(x(1000, ""), x(498, "a")), Array(x(1000, ""), x(498, "b")), 0},
		{Array(x(1499, ""), Null()), Array(x(1499, ""), Bool(false)), 0}, // the type is cut too
		{Array(x(1499, "")), Array(x(1499, ""), Null()), -1},
		{Array(x(1498, ""), Array(), Bool(false)), Array(x(1498, ""), Array(), Bool(true)), -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%.40v…, %.40v…) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestTruncatedAndIndexSize(t *testing.T) {
	x := func(n int, tail string) Value { return String(strings.Repeat("x", n) + tail) }
	for _, c := range []struct {
		v         Value
		cut       bool
		indexSize int
	}{
		{x(1499, ""), false, 1500},
		{x(1500, ""), true, 1501},
		{x(1498, "é"), false, 1501}, // over the limit but whole: production refuses it as a cursor
		{Array(x(1492, ""), Int(1)), false, 1501},
		{Array(x(1492, ""), Vector(1)), true, 1501},
		{Array(x(1498, ""), Array(), Array()), false, 1499},
		{Array(x(1499, ""), Array()), true, 1500}, // ties with [x×1499, null] (tr-empty-array-or-null)
		{Vector(make([]float64, 187)...), false, 1496},
		{Vector(make([]float64, 188)...), true, 1504},
	} {
		if got := Truncated(c.v); got != c.cut {
			t.Errorf("Truncated(%.30v…) = %v", c.v, got)
		}
		if got := IndexSize(c.v); got != c.indexSize {
			t.Errorf("IndexSize(%.30v…) = %d, want %d", c.v, got, c.indexSize)
		}
	}
}

func TestFieldPathRoundTrip(t *testing.T) {
	for _, c := range []struct {
		in   string
		segs FieldPath
		out  string
	}{
		{"a.b", FieldPath{"a", "b"}, "a.b"},
		{"`x.y`", FieldPath{"x.y"}, "`x.y`"},
		{"`\\``", FieldPath{"`"}, "`\\``"},
		{"`b\\\\c`", FieldPath{`b\c`}, "`b\\\\c`"},
		{"`\\a`", FieldPath{"a"}, "a"},
		{"`é`.`1n`._u", FieldPath{"é", "1n", "_u"}, "`é`.`1n`._u"},
		{"__name__", FieldPath{"__name__"}, "__name__"},
	} {
		p, err := ParseFieldPath(c.in)
		if err != nil || !slices.Equal(p, c.segs) || p.String() != c.out {
			t.Errorf("ParseFieldPath(%q) = %q, %v; String %q", c.in, p, err, p.String())
		}
	}
	for _, bad := range []string{"a..b", ".a", "a.", "``", "`a", "é", "1n", "a b", "`a`b"} {
		if _, err := ParseFieldPath(bad); err == nil {
			t.Errorf("ParseFieldPath(%q) accepted", bad)
		}
	}
}

func TestJavaDouble(t *testing.T) {
	for f, want := range map[float64]string{91: "91.0", -90.5: "-90.5", 1e10: "1.0E10", 1.5e-5: "1.5E-5", 1e7: "1.0E7",
		9999999: "9999999.0", 0.001: "0.001", math.Inf(1): "Infinity", math.Inf(-1): "-Infinity", 1e300: "1.0E300"} {
		if got := javaDouble(f); got != want {
			t.Errorf("javaDouble(%v) = %q, want %q", f, got, want)
		}
	}
	if got := javaDouble(math.NaN()); got != "NaN" {
		t.Errorf("javaDouble(NaN) = %q", got)
	}
}

func TestMapKeepsLastOfEqualNames(t *testing.T) {
	m := Map(Field{"b", Int(1)}, Field{"a", Int(2)}, Field{"b", Int(3)})
	if got, _ := m.Get("b"); got.IntValue() != 3 || len(m.Fields()) != 2 || m.Fields()[0].Name != "a" {
		t.Errorf("Map = %v", m)
	}
}

// TestDecodeKeyDepth: a forged key of a million nested arrays, or of maps nested in maps, is
// refused with an error, not a stack overflow; MaxDepth+1 levels still decode.
func TestDecodeKeyDepth(t *testing.T) {
	deep := func(open []byte, n int) []byte {
		return append([]byte{KeyVersion}, bytes.Repeat(open, n)...)
	}
	for name, k := range map[string][]byte{
		"arrays": deep([]byte{tagArray}, 1<<20),
		"maps":   deep([]byte{tagMap, item, 'a', 0x00, 0x01}, 1<<20),
	} {
		if _, err := DecodeKey(k); !errors.Is(err, errBadKey) {
			t.Errorf("%s: DecodeKey = %v", name, err)
		}
	}
	v := Int(1)
	for range maxKeyDepth {
		v = Map(Field{"a", Array(v)})
	}
	if _, err := DecodeKey(Key(v)); !errors.Is(err, errBadKey) {
		t.Errorf("%d levels: %v", 2*maxKeyDepth, err)
	}
	v = Int(1)
	for range maxKeyDepth {
		v = Map(Field{"a", v})
	}
	if d, err := DecodeKey(Key(v)); err != nil || Compare(d, v) != 0 {
		t.Errorf("%d maps: DecodeKey = %v, %v", maxKeyDepth, d, err)
	}
}

// TestApplyMaskManyPaths: an update mask of many paths costs about linear time (it was
// quadratic: 32,000 top-level paths took half a minute).
func TestApplyMaskManyPaths(t *testing.T) {
	for _, n := range []int{32_000, 100_000} {
		old, update, paths := manyPaths(n)
		start := time.Now()
		got := ApplyMask(old, update, paths)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%d paths took %v", n, d)
		}
		// Half the fields take update's value, the masked ones update lacks are deleted, and
		// the unmasked ones stay.
		if len(got.Fields()) != n {
			t.Fatalf("%d paths: %d fields", n, len(got.Fields()))
		}
		for i, f := range got.Fields() {
			if f.Value.IntValue() != int64(-i-1) && f.Value.IntValue() != int64(i+1) {
				t.Fatalf("%d paths: field %s = %v", n, f.Name, f.Value)
			}
		}
	}
}

// manyPaths builds a document of n fields f000000…, an update of the even ones negated and
// nested, and a mask naming each even field and each odd field's child.
func manyPaths(n int) (old, update Value, paths []FieldPath) {
	fs := make([]Field, n)
	var us []Field
	for i := range n {
		name := "f" + strconv.Itoa(1_000_000+i)
		fs[i] = Field{name, Int(int64(i + 1))}
		if i%2 == 0 {
			fs[i].Value = Int(int64(-i - 1))
			us = append(us, Field{name, Int(int64(-i - 1))})
			paths = append(paths, FieldPath{name})
		} else {
			paths = append(paths, FieldPath{name, "x"})
		}
	}
	return Map(fs...), Map(us...), paths
}

func BenchmarkApplyMask(b *testing.B) {
	old, update, paths := manyPaths(100_000)
	for b.Loop() {
		ApplyMask(old, update, paths)
	}
}

// TestApplyMaskTree covers the cases the corpus does not: deep paths, a path and its child,
// paths through a scalar, and an untouched document returned as it was.
func TestApplyMaskTree(t *testing.T) {
	doc := Map(Field{"a", Map(Field{"b", Int(1)}, Field{"c", Int(2)})}, Field{"s", Int(3)}, Field{"v", Vector(1, 2)})
	p := func(s ...string) FieldPath { return FieldPath(s) }
	for _, c := range []struct {
		update Value
		paths  []FieldPath
		want   Value
	}{
		{Map(), []FieldPath{p("a", "b"), p("a", "b", "z")}, Map(Field{"a", Map(Field{"c", Int(2)})}, Field{"s", Int(3)}, Field{"v", Vector(1, 2)})},
		{Map(Field{"s", Map(Field{"t", Int(9)})}), []FieldPath{p("s", "t"), p("s", "u")}, Map(Field{"a", Map(Field{"b", Int(1)}, Field{"c", Int(2)})}, Field{"s", Map(Field{"t", Int(9)})}, Field{"v", Vector(1, 2)})},
		{Map(), []FieldPath{p("s", "t"), p("n", "m"), p("v", "value")}, doc},
		{Map(Field{"n", Map(Field{"m", Map(Field{"o", Int(1)})})}), []FieldPath{p("n", "m", "o"), p("n", "q")}, Map(Field{"a", Map(Field{"b", Int(1)}, Field{"c", Int(2)})}, Field{"n", Map(Field{"m", Map(Field{"o", Int(1)})})}, Field{"s", Int(3)}, Field{"v", Vector(1, 2)})},
		{Map(Field{"a", Int(5)}), []FieldPath{p("a"), p("a", "b"), p(DocumentID)}, Map(Field{"a", Int(5)}, Field{"s", Int(3)}, Field{"v", Vector(1, 2)})},
	} {
		if got := ApplyMask(doc, c.update, c.paths); !Identical(got, c.want) {
			t.Errorf("ApplyMask(%v, %q) = %v, want %v", c.update, c.paths, got, c.want)
		}
	}
}

// TestDecimal128Strings: parsing follows Java's BigDecimal syntax and the MongoDB driver's
// exact clamping; the canonical strings are the driver's (the stored forms of values-bson.json
// are checked by TestBSONStoredAsProduction).
func TestDecimal128Strings(t *testing.T) {
	for in, want := range map[string]string{
		"1.": "1", ".5": "0.5", "-.5": "-0.5", "+0": "0", "-0.00": "-0.00", "1e+2": "1E+2", "1E-2": "0.01", "123.456e1": "1234.56",
		"0.0000010": "0.0000010", "1E-7": "1E-7", "-NAN": "NaN", "-INF": "-Infinity", "+Infinity": "Infinity",
		"1E2147483647": "", "1E-2147483648": "", "1E-6176": "1E-6176", "10E-6177": "1E-6176", "100E+6110": "1.00E+6112",
		"1e": "", "e5": "", "1e+": "", "+-1": "", "1.2.3": "", "1_0": "", "": "", "-": "", ".": "", "1e5.5": "", "0x10": "",
		"99999999999999999999999999999999990": "9.999999999999999999999999999999999E+34",
		"99999999999999999999999999999999999": "",
	} {
		v, ok := Decimal128(in)
		switch {
		case want == "" && ok:
			t.Errorf("Decimal128(%q) = %v, want refused", in, v)
		case want != "" && !ok:
			t.Errorf("Decimal128(%q) refused, want %s", in, want)
		case ok:
			if got, _ := v.Get("__decimal128__"); got.StringValue() != want {
				t.Errorf("Decimal128(%q) = %q, want %q", in, got.StringValue(), want)
			}
		}
	}
}

// TestBSONShapes: each constructor builds the shape BSONKind recognises, and a malformed or
// widened map is no shape.
func TestBSONShapes(t *testing.T) {
	dec, _ := Decimal128("1.5")
	for want, v := range map[BSONKind]Value{BSONMinKey: MinKey(), BSONMaxKey: MaxKey(), BSONInt32: Int32(-7), BSONDecimal128: dec,
		BSONTimestamp: BSONTimestampOf(1<<32-1, 2), BSONBinary: BSONBinaryOf(128, []byte{1}), BSONObjectID: ObjectID([12]byte{0xab}),
		BSONRegex: Regex("a.*", "imsux")} {
		if got := v.BSONKind(); got != want {
			t.Errorf("%v: BSONKind %v, want %v", v, got, want)
		}
		if err := Validate(Map(Field{"v", v})); err != nil {
			t.Errorf("%v: Validate: %v", v, err)
		}
	}
	for _, v := range []Value{Map(Field{"__int__", Int(1)}, Field{"b", Int(1)}), Map(Field{"__int__", String("1")}),
		Map(Field{"__binary__", Bytes([]byte{0, 1})}), Map(Field{"__oid__", String("ABCDEF0123456789abcdef01")}), Map()} {
		if got := v.BSONKind(); got != NotBSON {
			t.Errorf("%v: BSONKind %v", v, got)
		}
	}
	if got := javaGrouped(-1234567); got != "-1,234,567" {
		t.Errorf("javaGrouped = %q", got)
	}
}
