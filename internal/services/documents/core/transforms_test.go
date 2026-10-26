package core

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

var negZero = value.Double(math.Copysign(0, -1))

// transformCase applies one transform to a document's fields and checks the field and the result.
type transformCase struct {
	name   string
	fields value.Value
	t      Transform
	field  value.Value // the value at t.Path after
	result value.Value
}

func runTransforms(t *testing.T, cases []transformCase) {
	t.Helper()
	now := RequestTime(time.UnixMicro(1793042888857123))
	for _, c := range cases {
		out, res, err := c.t.apply(c.fields, now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, ok := lookup(out, c.t.Path)
		if !ok || !value.Identical(got, c.field) || !value.Identical(res, c.result) {
			t.Errorf("%s: field %v (found %v), result %v; want %v, %v", c.name, got, ok, res, c.field, c.result)
		}
	}
}

func tr(kind TransformKind, path string, operand value.Value) Transform {
	p, err := value.ParseFieldPath(path)
	if err != nil {
		panic(err)
	}
	return Transform{Path: p, Kind: kind, Operand: operand}
}

func TestIncrement(t *testing.T) {
	i, d := value.Int, value.Double
	inc := func(path string, n value.Value) Transform { return tr(TransformIncrement, path, n) }
	runTransforms(t, []transformCase{
		{"ints", doc("n", i(2)), inc("n", i(3)), i(5), i(5)},
		{"negative", doc("n", i(2)), inc("n", i(-3)), i(-1), i(-1)},
		{"overflow saturates at max", doc("n", i(math.MaxInt64-1)), inc("n", i(2)), i(math.MaxInt64), i(math.MaxInt64)},
		{"overflow saturates at min", doc("n", i(math.MinInt64+1)), inc("n", i(-2)), i(math.MinInt64), i(math.MinInt64)},
		{"max plus max", doc("n", i(math.MaxInt64)), inc("n", i(math.MaxInt64)), i(math.MaxInt64), i(math.MaxInt64)},
		{"min plus min", doc("n", i(math.MinInt64)), inc("n", i(math.MinInt64)), i(math.MinInt64), i(math.MinInt64)},
		{"max plus min", doc("n", i(math.MaxInt64)), inc("n", i(math.MinInt64)), i(-1), i(-1)},
		{"int plus double", doc("n", i(1)), inc("n", d(0.5)), d(1.5), d(1.5)},
		{"double plus int", doc("n", d(0.5)), inc("n", i(1)), d(1.5), d(1.5)},
		{"big int as double", doc("n", i(1<<53+1)), inc("n", d(0)), d(1 << 53), d(1 << 53)},
		{"doubles overflow to infinity", doc("n", d(math.MaxFloat64)), inc("n", d(math.MaxFloat64)), d(math.Inf(1)), d(math.Inf(1))},
		{"NaN field", doc("n", d(math.NaN())), inc("n", i(1)), d(math.NaN()), d(math.NaN())},
		{"NaN operand", doc("n", i(1)), inc("n", d(math.NaN())), d(math.NaN()), d(math.NaN())},
		{"-0.0 plus -0.0", doc("n", negZero), inc("n", negZero), negZero, negZero},
		{"-0.0 plus 0", doc("n", negZero), inc("n", i(0)), d(0), d(0)},
		{"0.0 plus -0.0", doc("n", d(0)), inc("n", negZero), d(0), d(0)},
		{"int 0 plus -0.0", doc("n", i(0)), inc("n", negZero), d(0), d(0)},
		{"missing plus -0.0", doc(), inc("n", negZero), negZero, negZero},
		{"missing field", doc(), inc("n", i(7)), i(7), i(7)},
		{"string field", doc("n", value.String("1")), inc("n", i(7)), i(7), i(7)},
		{"null field", doc("n", value.Null()), inc("n", d(1.5)), d(1.5), d(1.5)},
		{"nested, maps made", doc(), inc("a.b.c", i(1)), i(1), i(1)},
		{"through a scalar", doc("a", i(5)), inc("a.b", i(1)), i(1), i(1)},
		{"into a vector", doc("v", value.Vector(1, 2)), inc("v.value", i(1)), i(1), i(1)},
	})
}

// Standard refuses a numeric transform of a field that holds an Enterprise BSON int32 or
// Decimal128 (f2-transforms.json bson.on_int32_field, bson.on_decimal_field); an array transform
// replaces it as any other non-array.
func TestTransformOfBSONNumber(t *testing.T) {
	now := RequestTime(time.UnixMicro(1793042888857123))
	dec, ok := value.Decimal128("1.5")
	if !ok {
		t.Fatal("Decimal128")
	}
	for _, c := range []struct {
		field value.Value
		kind  TransformKind
		want  string
	}{
		{value.Int32(7), TransformIncrement, "Value type INT is not allowed"},
		{value.Int32(7), TransformMinimum, "Value type INT is not allowed"},
		{dec, TransformMaximum, "Value type DECIMAL128 is not allowed"},
	} {
		_, _, err := tr(c.kind, "n", value.Int(1)).apply(doc("n", c.field), now)
		if e, ok := errors.AsType[*Error](err); !ok || e.Code != InvalidArgument || e.Message != c.want {
			t.Errorf("%v on %v: %v, want %q", c.kind, c.field, err, c.want)
		}
	}
	out, _, err := tr(TransformAppendMissing, "n", value.Array(value.Int(1))).apply(doc("n", value.Int32(7)), now)
	if got, _ := lookup(out, value.FieldPath{"n"}); err != nil || !value.Identical(got, value.Array(value.Int(1))) {
		t.Errorf("append on an int32: %v, %v", got, err)
	}
}

func TestMaximumMinimum(t *testing.T) {
	i, d := value.Int, value.Double
	maxT := func(n value.Value) Transform { return tr(TransformMaximum, "n", n) }
	minT := func(n value.Value) Transform { return tr(TransformMinimum, "n", n) }
	nan := d(math.NaN())
	runTransforms(t, []transformCase{
		{"max ints", doc("n", i(2)), maxT(i(3)), i(3), i(3)},
		{"max keeps the field", doc("n", i(4)), maxT(i(3)), i(4), i(4)},
		{"max mixed takes the larger's type", doc("n", i(2)), maxT(d(2.5)), d(2.5), d(2.5)},
		{"max mixed keeps the int", doc("n", i(3)), maxT(d(2.5)), i(3), i(3)},
		{"max equivalent keeps the field", doc("n", i(3)), maxT(d(3)), i(3), i(3)},
		{"max equivalent keeps the double", doc("n", d(3)), maxT(i(3)), d(3), d(3)},
		{"max zeros keep the field", doc("n", negZero), maxT(i(0)), negZero, negZero},
		{"max zeros keep the int", doc("n", i(0)), maxT(negZero), i(0), i(0)},
		{"max NaN operand", doc("n", i(3)), maxT(nan), nan, nan},
		{"max NaN field", doc("n", nan), maxT(i(3)), nan, nan},
		{"max infinity", doc("n", i(math.MaxInt64)), maxT(d(math.Inf(1))), d(math.Inf(1)), d(math.Inf(1))},
		{"max exact past 2^53", doc("n", i(1<<53+1)), maxT(d(1 << 53)), i(1<<53 + 1), i(1<<53 + 1)},
		{"max missing", doc(), maxT(i(3)), i(3), i(3)},
		{"max non-numeric", doc("n", value.Bool(true)), maxT(i(-3)), i(-3), i(-3)},
		{"min ints", doc("n", i(2)), minT(i(3)), i(2), i(2)},
		{"min takes the operand", doc("n", i(4)), minT(i(3)), i(3), i(3)},
		{"min mixed takes the smaller's type", doc("n", i(3)), minT(d(2.5)), d(2.5), d(2.5)},
		{"min equivalent keeps the field", doc("n", d(3)), minT(i(3)), d(3), d(3)},
		{"min zeros keep the field", doc("n", i(0)), minT(negZero), i(0), i(0)},
		{"min NaN operand", doc("n", i(3)), minT(nan), nan, nan},
		{"min NaN field", doc("n", nan), minT(d(math.Inf(-1))), nan, nan},
		{"min missing", doc(), minT(d(-1)), d(-1), d(-1)},
		{"min non-numeric", doc("n", value.Array(i(1))), minT(i(9)), i(9), i(9)},
	})
}

func TestArrayTransforms(t *testing.T) {
	i, d, a := value.Int, value.Double, value.Array
	union := func(els ...value.Value) Transform { return tr(TransformAppendMissing, "a", a(els...)) }
	remove := func(els ...value.Value) Transform { return tr(TransformRemoveAll, "a", a(els...)) }
	null, nan := value.Null(), d(math.NaN())
	long := strings.Repeat("x", 2000)
	runTransforms(t, []transformCase{
		{"union appends the missing", doc("a", a(i(1), i(2))), union(i(2), i(3)), a(i(1), i(2), i(3)), null},
		{"union 1.0 is 1", doc("a", a(i(1))), union(d(1)), a(i(1)), null},
		{"union -0.0 is 0", doc("a", a(i(0))), union(negZero), a(i(0)), null},
		{"union NaN is NaN", doc("a", a(nan)), union(nan), a(nan), null},
		{"union null is null", doc("a", a(null)), union(null), a(null), null},
		{"union first of equivalents", doc("a", a()), union(i(1), d(1), i(2), i(1)), a(i(1), i(2)), null},
		{"union keeps the field's duplicates", doc("a", a(i(1), i(1))), union(i(1)), a(i(1), i(1)), null},
		{"union nested equality", doc("a", a(a(i(1)), doc("k", i(2)))), union(a(d(1)), doc("k", d(2)), doc("k", i(3))),
			a(a(i(1)), doc("k", i(2)), doc("k", i(3))), null},
		{"union strings past the index", doc("a", a(value.String(long+"a"))), union(value.String(long + "b")),
			a(value.String(long+"a"), value.String(long+"b")), null},
		{"union missing field", doc(), union(i(1), i(1)), a(i(1)), null},
		{"union non-array field", doc("a", i(5)), union(i(1)), a(i(1)), null},
		{"union nothing", doc(), union(), a(), null},
		{"remove every equivalent", doc("a", a(i(1), d(1), i(2), i(1))), remove(i(1)), a(i(2)), null},
		{"remove NaN and null", doc("a", a(nan, null, i(0))), remove(nan, null), a(i(0)), null},
		{"remove -0.0 removes 0", doc("a", a(i(0), d(0), negZero)), remove(negZero), a(), null},
		{"remove nested", doc("a", a(a(i(1), i(2)), a(i(2), i(1)))), remove(a(d(1), d(2))), a(a(i(2), i(1))), null},
		{"remove missing field", doc(), remove(i(1)), a(), null},
		{"remove non-array field", doc("a", value.String("x")), remove(i(1)), a(), null},
	})
}

// REQUEST_TIME is the commit_time at whole milliseconds (f1-write-results.json).
func TestServerTime(t *testing.T) {
	commit := time.UnixMicro(1793042888857999)
	runTransforms(t, []transformCase{
		{"millisecond", doc(), Transform{Path: value.FieldPath{"ts"}, Kind: TransformServerTime}, value.Timestamp(1793042888, 857000000), value.Timestamp(1793042888, 857000000)},
	})
	if got := RequestTime(commit); !value.Identical(got, value.Timestamp(1793042888, 857000000)) {
		t.Errorf("RequestTime %v", got)
	}
}

// A write's transforms apply after its fields, in order, and each has its result.
func TestPlanTransforms(t *testing.T) {
	p := ParsePath("c/d")
	locked := map[string]*lockedDoc{string(storedKey(nameKey(p))): {fields: doc("n", value.Int(1), "keep", value.Bool(true))}}
	commit := time.UnixMicro(1793042888857999)
	w := Write{Kind: WriteUpdate, Path: p, Masked: true, Mask: []value.FieldPath{{"x"}}, Fields: doc("x", value.Int(1)),
		Transforms: []Transform{
			tr(TransformIncrement, "n", value.Int(2)),
			tr(TransformServerTime, "ts", value.Value{}),
			tr(TransformIncrement, "n", value.Double(0.5)),
			tr(TransformAppendMissing, "x", value.Array(value.Int(1))),
		}}
	pl := newPlan([]Write{w})
	if !pl.serverTime {
		t.Fatal("serverTime not seen")
	}
	if err := pl.apply(testDB, locked, commit); err != nil {
		t.Fatal(err)
	}
	ts := RequestTime(commit)
	want := doc("keep", value.Bool(true), "n", value.Double(3.5), "ts", ts, "x", value.Array(value.Int(1)))
	if !value.Identical(pl.docs[0].fields, want) {
		t.Errorf("fields %v, want %v", pl.docs[0].fields, want)
	}
	res := pl.results(commit)[0]
	wantRes := []value.Value{value.Int(3), ts, value.Double(3.5), value.Null()}
	if len(res.TransformResults) != len(wantRes) {
		t.Fatalf("results %v", res.TransformResults)
	}
	for i := range wantRes {
		if !value.Identical(res.TransformResults[i], wantRes[i]) {
			t.Errorf("result %d: %v, want %v", i, res.TransformResults[i], wantRes[i])
		}
	}
	if !res.UpdateTime.Equal(commit) || !pl.docs[0].changed() {
		t.Errorf("update time %v, changed %v", res.UpdateTime, pl.docs[0].changed())
	}
	if m := pl.docs[0].mask; len(m) != 5 || m[0].String() != "x" || m[2].String() != "ts" {
		t.Errorf("changelog mask %v", m)
	}

	// A transform that changes nothing leaves the document unchanged.
	same := newPlan([]Write{{Kind: WriteUpdate, Path: p, Masked: true, Fields: value.Map(),
		Transforms: []Transform{tr(TransformMaximum, "n", value.Double(1))}}})
	if err := same.apply(testDB, locked, commit); err != nil || same.docs[0].changed() {
		t.Errorf("maximum of an equal value changed the document: %v", err)
	}
	// The document the transforms make is checked: depth and size.
	deep := value.Int(1)
	for range value.MaxDepth {
		deep = doc("m", deep)
	}
	tooDeep := newPlan([]Write{{Kind: WriteUpdate, Path: p, Fields: value.Map(),
		Transforms: []Transform{tr(TransformAppendMissing, "a", value.Array(deep))}}})
	if err := tooDeep.apply(testDB, nil, commit); err == nil {
		t.Error("a transform made a document too deep, and it passed")
	}
	big := value.String(strings.Repeat("x", value.MaxDocumentSize))
	tooBig := newPlan([]Write{{Kind: WriteUpdate, Path: p, Fields: value.Map(),
		Transforms: []Transform{tr(TransformAppendMissing, "a", value.Array(big))}}})
	if err := tooBig.apply(testDB, nil, commit); err == nil {
		t.Error("a transform made a document too large, and it passed")
	}
}

// applyTransforms is apply of each transform in order, whatever the paths (equal, apart, under a
// scalar or a missing map) and kinds: checked against the sequential apply on random writes.
func TestApplyTransformsSequential(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	paths := []string{"a", "b", "m.x", "m.y", "m.n.z", "s.q", "v.k"}
	vals := []value.Value{value.Int(1), value.Double(2.5), value.Int(-3), negZero, value.Double(math.NaN()), value.String("x"), value.Null()}
	arr := func() value.Value {
		els := make([]value.Value, rng.IntN(4))
		for i := range els {
			els[i] = vals[rng.IntN(len(vals))]
		}
		return value.Array(els...)
	}
	now := RequestTime(time.UnixMicro(1793042888857123))
	for range 2000 {
		fields := value.Map(
			value.Field{Name: "a", Value: value.Int(5)},
			value.Field{Name: "m", Value: value.Map(value.Field{Name: "x", Value: arr()})},
			value.Field{Name: "s", Value: value.String("scalar")},
			value.Field{Name: "z", Value: value.Bool(true)},
		)
		var ts []Transform
		for range 1 + rng.IntN(6) {
			p := paths[rng.IntN(len(paths))]
			// Keep the paths equal or apart, as the transport does.
			if slices.ContainsFunc(ts, func(u Transform) bool {
				q := u.Path.String()
				return q != p && (strings.HasPrefix(p, q+".") || strings.HasPrefix(q, p+"."))
			}) {
				continue
			}
			kind := TransformKind(1 + rng.IntN(6))
			var op value.Value
			switch kind {
			case TransformIncrement, TransformMaximum, TransformMinimum:
				op = vals[rng.IntN(4)]
			case TransformAppendMissing, TransformRemoveAll:
				op = arr()
			}
			ts = append(ts, tr(kind, p, op))
		}
		want, wantRes := fields, make([]value.Value, len(ts))
		for i, tf := range ts {
			var err error
			if want, wantRes[i], err = tf.apply(want, now); err != nil {
				t.Fatal(err)
			}
		}
		got, gotRes, err := applyTransforms(fields, ts, now)
		if err != nil {
			t.Fatal(err)
		}
		if !value.Identical(got, want) || !slices.EqualFunc(gotRes, wantRes, value.Identical) {
			t.Fatalf("%v on %v: got %v %v, want %v %v", ts, fields, got, gotRes, want, wantRes)
		}
	}
}

// The keyed element set answers as the scan does: equivalent numbers of any type are one, NaN
// is NaN, and long strings that share the index's cut are told apart.
func TestElementSetKeyed(t *testing.T) {
	long := strings.Repeat("x", 2000)
	els := []value.Value{value.Int(3), negZero, value.Double(math.NaN()), value.String(long + "a"), value.Null(),
		value.Array(value.Int(1)), value.Map(value.Field{Name: "k", Value: value.Double(1)})}
	probes := []value.Value{value.Double(3), value.Int(0), value.Double(math.NaN()), value.String(long + "a"),
		value.String(long + "b"), value.Null(), value.Array(value.Double(1)), value.Map(value.Field{Name: "k", Value: value.Int(1)}),
		value.Int(4), value.Bool(false)}
	keyed := newElementSet(els, 1000)
	if keyed.byKey == nil {
		t.Fatal("not keyed")
	}
	for _, p := range probes {
		want := slices.ContainsFunc(els, func(e value.Value) bool { return value.Equivalent(e, p) })
		if got := keyed.holds(p); got != want {
			t.Errorf("%v: got %v, want %v", p, got, want)
		}
	}
}

// The worst writes a 16 MiB request can carry cost about their size, not its square: tens of
// thousands of transforms of one write, and array transforms of a million elements (F2 review M1).
func TestTransformsWorstCase(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	now := RequestTime(time.UnixMicro(1793042888857123))
	ts := make([]Transform, maxTransformsBench)
	for i := range ts {
		ts[i] = tr(TransformIncrement, "f"+strconv.Itoa(i), value.Int(1))
	}
	start := time.Now()
	if _, _, err := applyTransforms(value.Map(), ts, now); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("%d transforms took %v", len(ts), d)
	}
	els := make([]value.Value, 1<<20)
	for i := range els {
		els[i] = value.Int(int64(i))
	}
	big := value.Array(els...)
	start = time.Now()
	base := doc("a", big)
	for _, kind := range []TransformKind{TransformAppendMissing, TransformRemoveAll} {
		if _, _, err := tr(kind, "a", big).apply(base, now); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("array transforms of %d elements took %v", len(els), d)
	}
}

// maxTransformsBench is about as many transforms as a 16 MiB request holds.
const maxTransformsBench = 200_000

func BenchmarkApplyTransforms(b *testing.B) {
	now := RequestTime(time.UnixMicro(1793042888857123))
	ts := make([]Transform, 20_000)
	for i := range ts {
		ts[i] = tr(TransformIncrement, "f"+strconv.Itoa(i), value.Int(1))
	}
	for b.Loop() {
		if _, _, err := applyTransforms(value.Map(), ts, now); err != nil {
			b.Fatal(err)
		}
	}
}
