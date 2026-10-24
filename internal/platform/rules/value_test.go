package rules

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// This file is S1-B's (rules-layout.md): the cases of the value model, and the helpers the
// library's tests share. S1-B tests the library on values directly, without the parser.
//
// Provenance of the cases, as the comments name it:
//   - "production (Rules Test API, S0-A)": the lead's summary of S0-A's oracle, 2026-10-24.
//   - "Pyric <scenario>": Pyric's recordings of the Rules Test API (production), 2026-07 to
//     2026-10, packages/conformance/observations/{firestore,storage}-rules.
//   - "hidane": hidane's recordings of the official emulator (v1.22.0), commit abd4664.
//   - "reference": firebase.google.com/docs/reference/rules, read 2026-10-24.

// newTestMeter returns a meter with the default limits and a fresh pattern cache.
func newTestMeter() *meter {
	lim := DefaultLimits()
	return newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
}

// Short constructors for tables.
func vn() Value                  { return Null() }
func vb(b bool) Value            { return Bool(b) }
func vi(i int64) Value           { return Int(i) }
func vf(f float64) Value         { return Float(f) }
func vs(s string) Value          { return String(s) }
func vbytes(s string) Value      { return Bytes([]byte(s)) }
func vl(vs ...Value) Value       { return List(vs) }
func vp(segs ...string) Value    { return PathOf(segs) }
func vdur(d time.Duration) Value { return Duration(d) }
func vts(s string) Value {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return Timestamp(t)
}

// vm returns a map literal of alternating keys and values, in the order written.
func vm(kv ...any) Value {
	var keys, vals []Value
	for i := 0; i < len(kv); i += 2 {
		keys = append(keys, String(kv[i].(string)))
		vals = append(vals, kv[i+1].(Value))
	}
	v, err := buildMap(newTestMeter(), keys, vals)
	if err != nil {
		panic(err)
	}
	return v
}

// vset returns the set of items, as toSet() builds it.
func vset(items ...Value) Value {
	v, err := callMethod(newTestMeter(), List(items), "toSet", nil)
	if err != nil {
		panic(err)
	}
	return v
}

var negZero = math.Copysign(0, -1)

// testExpr is an expression of a case, evaluated on a meter.
type testExpr func(m *meter) (Value, error)

func vLit(v Value) testExpr { return func(*meter) (Value, error) { return v, nil } }

func vOp(x Value, tok token, y Value) testExpr {
	return func(m *meter) (Value, error) { return binary(m, tok, x, y) }
}

func vUn(tok token, x Value) testExpr {
	return func(m *meter) (Value, error) { return unary(m, tok, x) }
}

func vCall(recv Value, name string, args ...Value) testExpr {
	return func(m *meter) (Value, error) { return callMethod(m, recv, name, args) }
}

func vFn(name string, args ...Value) testExpr {
	return func(m *meter) (Value, error) { return callFunction(m, name, args) }
}

// then calls name on what e returns.
func vThen(e testExpr, name string, args ...Value) testExpr {
	return func(m *meter) (Value, error) {
		v, err := e(m)
		if err != nil {
			return Value{}, err
		}
		return callMethod(m, v, name, args)
	}
}

// opOn applies tok to what two expressions return.
func vOpOn(x testExpr, tok token, y testExpr) testExpr {
	return func(m *meter) (Value, error) {
		a, err := x(m)
		if err != nil {
			return Value{}, err
		}
		b, err := y(m)
		if err != nil {
			return Value{}, err
		}
		return binary(m, tok, a, b)
	}
}

// valueCase is one case: e must return want, or, when want is the zero Value, an evaluation
// error whose message contains err.
type valueCase struct {
	name string
	e    testExpr
	want Value
	err  string
}

func runValueCases(t *testing.T, cases []valueCase) {
	t.Helper()
	for _, c := range cases {
		m := newTestMeter()
		got, err := c.e(m)
		if c.want.kind == kindInvalid {
			var ee *evalError
			if !errors.As(err, &ee) {
				t.Errorf("%s: got %s, %v; want an evaluation error", c.name, showValue(got), err)
			} else if !strings.Contains(ee.msg, c.err) {
				t.Errorf("%s: error %q, want one containing %q", c.name, ee.msg, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: error %v, want %s", c.name, err, showValue(c.want))
			continue
		}
		if !sameValue(got, c.want) {
			t.Errorf("%s: got %s, want %s", c.name, showValue(got), showValue(c.want))
		}
	}
}

// same is the strictest equality: kinds, ±0 and NaN included; sets by E4.
func sameValue(a, b Value) bool {
	if a.kind == KindFloat && b.kind == KindFloat {
		x, y := a.float(), b.float()
		return x == y && math.Signbit(x) == math.Signbit(y) || math.IsNaN(x) && math.IsNaN(y)
	}
	if a.kind == KindMapDiff || b.kind == KindMapDiff {
		return false
	}
	eq, err := equal(newTestMeter(), relContainer, a, b)
	return err == nil && eq
}

// show writes a value for a failure message.
func showValue(v Value) string {
	switch v.kind {
	case KindString:
		return "'" + v.str() + "'"
	case KindBytes:
		return "b'" + v.str() + "'"
	case KindList, KindSet:
		parts := make([]string, len(v.items()))
		for i, e := range v.items() {
			parts[i] = showValue(e)
		}
		if v.kind == KindSet {
			return "set[" + strings.Join(parts, ", ") + "]"
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindMap:
		mv := v.mapv()
		parts := make([]string, len(mv.keys))
		for i, k := range mv.keys {
			parts[i] = k + ": " + showValue(mv.entries[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case KindFloat:
		return javaDouble(v.float())
	case KindPath:
		return pathString(v.segments())
	case KindTimestamp:
		return v.instant().Format(time.RFC3339Nano)
	case kindInvalid:
		return "<none>"
	}
	s, err := toString(v)
	if err != nil {
		return v.kind.String()
	}
	return s
}

// A rules duration reaches ±10,000 years; AsDuration answers only for what time.Duration holds,
// to the nanosecond at both ends.
func TestAsDurationRange(t *testing.T) {
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	for _, c := range []struct {
		d    duration
		want time.Duration
		ok   bool
	}{
		{duration{maxSeconds, 854_775_807}, math.MaxInt64, true},
		{duration{maxSeconds, 854_775_808}, 0, false},
		{duration{maxSeconds, 999_999_999}, 0, false},
		{duration{maxSeconds + 1, 0}, 0, false},
		{duration{-maxSeconds, -854_775_808}, math.MinInt64, true},
		{duration{-maxSeconds, -854_775_809}, 0, false},
		{duration{-maxSeconds, -999_999_999}, 0, false},
		{duration{10_000 * 366 * 86400, 0}, 0, false},
		{duration{-1, -500_000_000}, -1500 * time.Millisecond, true},
	} {
		got, ok := Value{kind: KindDuration, x: c.d}.AsDuration()
		if ok != c.ok || got != c.want {
			t.Errorf("%+v: AsDuration() = %v, %v; want %v, %v", c.d, got, ok, c.want, c.ok)
		}
	}
}

// A map the services build has its entries in the order of their keys; a literal's are in the
// order written, a repeated key keeping its first place and its last value (Pyric keyOrder).
func TestMapOrder(t *testing.T) {
	built := Map(map[string]Value{"b": Int(1), "a": Int(2), "c": Int(3)})
	if got := built.mapv().keys; strings.Join(got, ",") != "a,b,c" {
		t.Errorf("Map's order = %v, want the keys sorted", got)
	}
	runValueCases(t, []valueCase{
		{"literal values() in written order", vCall(vm("b", vi(1), "a", vi(2)), "values"), vl(vi(1), vi(2)), ""},
		{"repeated key: first place, last value", vCall(vm("a", vi(1), "b", vi(2), "a", vi(3)), "values"), vl(vi(3), vi(2)), ""},
		{"service map values() by key", vCall(built, "values"), vl(vi(2), vi(1), vi(3)), ""},
	})
	m := newTestMeter()
	if _, err := buildMap(m, []Value{Int(1)}, []Value{Int(2)}); err == nil || err.Error() != "Type error. Received: [int] Expected: [string]." {
		// Pyric simulator-language-semantics FS-SIM-11: a map literal with an int key.
		t.Errorf("an int key = %v, want the type error", err)
	}
	if _, err := buildMap(m, []Value{String("a")}, nil); err == nil {
		t.Error("keys without values built a map")
	}
	l, err := buildList(m, []Value{Int(1)})
	if err != nil || !sameValue(l, vl(vi(1))) {
		t.Errorf("buildList = %s, %v", showValue(l), err)
	}
}

// The builders charge the meter before they build: a value past the caps is an abort.
func TestBuildersCharge(t *testing.T) {
	lim := DefaultLimits()
	lim.ValueElements, lim.AllocBytes, lim.ValueBytes = 3, 4096, 64
	m := newMeter(&lim, NewInflight(1<<20), newRegexCache(&lim))
	var a *abort
	if _, err := buildList(m, []Value{Int(1), Int(2), Int(3), Int(4)}); !errors.As(err, &a) {
		t.Errorf("a list past value_elements = %v, want an abort", err)
	}
	if _, err := buildMap(m, []Value{String("a"), String("b"), String("c"), String("d")}, []Value{Null(), Null(), Null(), Null()}); !errors.As(err, &a) {
		t.Errorf("a map past value_elements = %v, want an abort", err)
	}
	if _, err := binary(m, tokPlus, String(strings.Repeat("x", 40)), String(strings.Repeat("y", 40))); !errors.As(err, &a) {
		t.Errorf("a string past value_bytes = %v, want an abort", err)
	}
	if m.allocated != 0 {
		t.Errorf("refused builders charged %d bytes", m.allocated)
	}
	if _, err := buildList(m, []Value{Int(1)}); err != nil || m.allocated == 0 {
		t.Errorf("a list within the caps = %v, charged %d", err, m.allocated)
	}
}

func TestTypeIs(t *testing.T) {
	for _, c := range []struct {
		v    Value
		typ  string
		want bool
	}{
		{vi(1), "int", true}, {vi(1), "float", false}, {vf(1), "float", true}, {vf(1), "int", false},
		// Pyric int-float-and-division: is number holds for an int and a float, not a string or null.
		{vi(1), "number", true}, {vf(1.5), "number", true}, {vs("1"), "number", false}, {vn(), "number", false},
		{vs("a"), "string", true}, {vb(true), "bool", true}, {vbytes("a"), "bytes", true},
		{vl(), "list", true}, {vm(), "map", true}, {vm(), "list", false},
		{vts("2025-06-15T13:45:30Z"), "timestamp", true}, {vdur(time.Second), "duration", true},
		{LatLng(1, 2), "latlng", true}, {vp("a"), "path", true},
		// Pyric path-constructor-and-bind: a path is not a string or a map.
		{vp("a", "b"), "string", false}, {vp("a", "b"), "map", false},
		// hidane: a set is never "set" (the emulator), and nothing is a vector; keys() is a list,
		// not a set (Pyric required-fields-and-mapdiff).
		{vset(vi(1)), "set", false}, {vset(vi(1)), "list", false}, {vl(), "vector", false},
		{vn(), "map", false},
	} {
		got, err := typeIs(c.v, c.typ)
		if err != nil || got.boolean() != c.want {
			t.Errorf("%s is %s = %s, %v; want %v", showValue(c.v), c.typ, showValue(got), err, c.want)
		}
	}
	if _, err := typeIs(Int(1), "integer"); err == nil {
		t.Error("an unknown type name is not an error")
	}
	var ee *evalError
	if _, err := typeIs(Value{kind: KindUnknown}, "int"); !errors.As(err, &ee) || ee.msg != "" {
		t.Errorf("is on an Unknown = %v, want an error without a message", err)
	}
}

// Every operation on a reserved value, or on the zero Value, is an error without a message: a
// denial, never an allowance (ADR-021 §2.4).
func TestReservedValuesAreErrors(t *testing.T) {
	m := newTestMeter()
	for _, r := range []Value{{kind: KindConstraint}, {kind: KindUnknown}, {}} {
		checks := map[string]func() (Value, error){
			"==":          func() (Value, error) { return binary(m, tokEQ, r, Int(1)) },
			"!= right":    func() (Value, error) { return binary(m, tokNE, Int(1), r) },
			"<":           func() (Value, error) { return binary(m, tokLT, r, Int(1)) },
			"in":          func() (Value, error) { return binary(m, tokIn, r, List(nil)) },
			"!":           func() (Value, error) { return unary(m, tokNot, r) },
			"field":       func() (Value, error) { return selectField(m, r, "a") },
			"index":       func() (Value, error) { return selectIndex(m, r, Int(0)) },
			"slice":       func() (Value, error) { return selectSlice(m, r, nil, nil) },
			"method":      func() (Value, error) { return callMethod(m, r, "size", nil) },
			"argument":    func() (Value, error) { return callMethod(m, List(nil), "hasAny", []Value{r}) },
			"function":    func() (Value, error) { return callFunction(m, "string", []Value{r}) },
			"map entry":   func() (Value, error) { return selectField(m, Map(map[string]Value{"a": r}), "a") },
			"list elem":   func() (Value, error) { return selectIndex(m, List([]Value{r}), Int(0)) },
			"path part":   func() (Value, error) { return buildPath(m, []pathPart{{value: r, isVal: true}}) },
			"map literal": func() (Value, error) { return buildMap(m, []Value{r}, []Value{Null()}) },
		}
		for name, check := range checks {
			v, err := check()
			var ee *evalError
			if !errors.As(err, &ee) || ee.msg != "" {
				t.Errorf("%v %s = %s, %v; want an error without a message", r.kind, name, showValue(v), err)
			}
		}
	}
}
