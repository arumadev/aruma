package rules

import (
	"math"
	"testing"
	"time"
)

// The four equality relations (ADR-021 §2.3), with Pyric's production cases by scenario and
// hidane's emulator cases where Pyric has none.

// E1: == and != at the top of an expression.
func TestEqualityTop(t *testing.T) {
	nan := math.NaN()
	runValueCases(t, []valueCase{
		// Pyric cross-type-operator-overloads "equalityIdentity", list-map-literals-and-slice "equality".
		{"an int equals a float of the same value", vOp(vi(1), tokEQ, vf(1)), vb(true), ""},
		{"an int zero equals a negative float zero", vOp(vi(0), tokEQ, vf(negZero)), vb(true), ""},
		{"0.0 == -0.0 (IEEE)", vOp(vf(0), tokEQ, vf(negZero)), vb(true), ""},
		{"NaN does not equal NaN", vOp(vf(nan), tokEQ, vf(nan)), vb(false), ""},
		{"NaN != NaN", vOp(vf(nan), tokNE, vf(nan)), vb(true), ""},
		{"an element read from a List equals the float", vOpOn(func(m *meter) (Value, error) { return selectIndex(m, vl(vi(1)), vi(0)) }, tokEQ, vLit(vf(1))), vb(true), ""},
		// Pyric ordering-operand-types: values of other types are unequal, not an error.
		{"int == string", vOp(vi(1), tokEQ, vs("1")), vb(false), ""},
		{"1 != '1'", vOp(vi(1), tokNE, vs("1")), vb(true), ""},
		{"bool != int", vOp(vb(true), tokNE, vi(1)), vb(true), ""},
		{"timestamp != int", vOp(vts("2025-01-01T00:00:00Z"), tokNE, vi(1)), vb(true), ""},
		{"list != map", vOp(vl(vs("a")), tokNE, vm("k", vs("w"))), vb(true), ""},
		{"null != int", vOp(vn(), tokNE, vi(1)), vb(true), ""},
		{"null == null", vOp(vn(), tokEQ, vn()), vb(true), ""},
		// Pyric bytes-literal: bytes are not strings.
		{"b'abc' == 'abc'", vOp(vbytes("abc"), tokEQ, vs("abc")), vb(false), ""},
		{"b'abc' == b'abc'", vOp(vbytes("abc"), tokEQ, vbytes("abc")), vb(true), ""},
		{"b'abc' == b'abd'", vOp(vbytes("abc"), tokEQ, vbytes("abd")), vb(false), ""},
		// Pyric path-constructor-and-bind.
		{"path equality", vOp(vp("users", "alice"), tokEQ, vp("users", "alice")), vb(true), ""},
		{"path inequality", vOp(vp("users", "alice"), tokNE, vp("users", "bob")), vb(true), ""},
		{"path != string", vOp(vp("a"), tokEQ, vs("/a")), vb(false), ""},
		{"timestamps by instant", vOp(vts("2025-01-01T01:00:00+01:00"), tokEQ, vts("2025-01-01T00:00:00Z")), vb(true), ""},
		{"durations", vOp(vdur(time.Hour), tokEQ, vdur(60*time.Minute)), vb(true), ""},
		{"latlng", vOp(LatLng(1, 2), tokEQ, LatLng(1, 2)), vb(true), ""},
		// hidane: two map diffs are never equal.
		{"map diffs", vOpOn(vCall(vm(), "diff", vm()), tokEQ, vCall(vm(), "diff", vm())), vb(false), ""},
	})
}

// E2: lists and maps under ==, at any depth.
func TestEqualityContainers(t *testing.T) {
	nan := math.NaN()
	runValueCases(t, []valueCase{
		// Pyric "equalityIdentity" / "equality": production (Rules Test API, S0-A) agrees that
		// [1] == [1.0] is false.
		{"a List of an int does not equal a List of a float", vOp(vl(vi(1)), tokEQ, vl(vf(1))), vb(false), ""},
		{"!= between them is true", vOp(vl(vi(1)), tokNE, vl(vf(1))), vb(true), ""},
		{"a List of a float does not equal a List of an int", vOp(vl(vf(1)), tokEQ, vl(vi(1))), vb(false), ""},
		{"one float element makes Lists unequal", vOp(vl(vi(1), vi(2)), tokEQ, vl(vi(1), vf(2))), vb(false), ""},
		{"nested Lists compare element types", vOp(vl(vl(vi(1))), tokEQ, vl(vl(vf(1)))), vb(false), ""},
		{"a Map of an int does not equal a Map of a float", vOp(vm("a", vi(1)), tokEQ, vm("a", vf(1))), vb(false), ""},
		{"a Map value List compares element types", vOp(vm("a", vl(vi(1))), tokEQ, vm("a", vl(vf(1)))), vb(false), ""},
		{"Lists of equal floats are equal", vOp(vl(vf(1.5)), tokEQ, vl(vf(1.5))), vb(true), ""},
		{"Maps of equal floats are equal", vOp(vm("a", vf(1.5)), tokEQ, vm("a", vf(1.5))), vb(true), ""},
		// ±0 are two values in a container. hidane (value.rs:317, emulator) says one; Pyric
		// (production) says two, and wins.
		{"a List of 0.0 does not equal a List of -0.0", vOp(vl(vf(0)), tokEQ, vl(vf(negZero))), vb(false), ""},
		{"Lists of -0.0 are equal", vOp(vl(vf(negZero)), tokEQ, vl(vf(negZero))), vb(true), ""},
		{"a Map of -0.0 does not equal a Map of 0.0", vOp(vm("a", vf(negZero)), tokEQ, vm("a", vf(0))), vb(false), ""},
		{"a List of NaN does not equal a List of NaN", vOp(vl(vf(nan)), tokEQ, vl(vf(nan))), vb(false), ""},
		{"Map.values() of an int does not equal a List of a float", vOpOn(vCall(vm("a", vi(1)), "values"), tokEQ, vLit(vl(vf(1)))), vb(false), ""},
		{"maps ignore order", vOp(vm("a", vi(1), "b", vi(2)), tokEQ, vm("b", vi(2), "a", vi(1))), vb(true), ""},
		{"maps of other keys", vOp(vm("a", vi(1)), tokEQ, vm("b", vi(1))), vb(false), ""},
		{"lists of other lengths", vOp(vl(vi(1)), tokEQ, vl(vi(1), vi(1))), vb(false), ""},
		// Pyric cross-type-operator-overloads: concat, not +, joins lists.
		{"[1].concat([2]) == [1, 2]", vOpOn(vCall(vl(vi(1)), "concat", vl(vi(2))), tokEQ, vLit(vl(vi(1), vi(2)))), vb(true), ""},
	})
}

// E3: elements of a list, for in and the list methods.
func TestEqualityListMembers(t *testing.T) {
	nan := math.NaN()
	runValueCases(t, []valueCase{
		// Pyric list-methods-concat-removeall-toset "listMethod" and stdlib-sets-and-mapdiff "list:".
		{"a float is not in a List of the equal int", vOp(vf(1), tokIn, vl(vi(1))), vb(false), ""},
		{"an int is not in a List of the equal float", vOp(vi(1), tokIn, vl(vf(1))), vb(false), ""},
		{"-0.0 is in a List of 0.0", vOp(vf(negZero), tokIn, vl(vf(0))), vb(true), ""},
		{"an int zero is not in a List of -0.0", vOp(vi(0), tokIn, vl(vf(negZero))), vb(false), ""},
		{"NaN is not in a List of NaN", vOp(vf(nan), tokIn, vl(vf(nan))), vb(false), ""},
		{"a List of an int is not in a List of a List of the equal float", vOp(vl(vi(1)), tokIn, vl(vl(vf(1)))), vb(false), ""},
		{"a Map of an int is not in a List of a Map of the equal float", vOp(vm("a", vi(1)), tokIn, vl(vm("a", vf(1)))), vb(false), ""},
		{"a List of 0.0 is not in a List of a List of -0.0", vOp(vl(vf(0)), tokIn, vl(vl(vf(negZero)))), vb(false), ""},
		{"a float is in a List of the same float", vOp(vf(1.5), tokIn, vl(vf(1.5))), vb(true), ""},
		{"hasAny: an int List has no float of the same value", vCall(vl(vi(1)), "hasAny", vl(vf(1))), vb(false), ""},
		{"hasAny: a float List has no int of the same value", vCall(vl(vf(1)), "hasAny", vl(vi(1))), vb(false), ""},
		{"hasAll: an int List does not have all of a float List", vCall(vl(vi(1), vi(2)), "hasAll", vl(vf(1))), vb(false), ""},
		{"hasOnly: an int List does not have only a float List", vCall(vl(vi(1)), "hasOnly", vl(vf(1))), vb(false), ""},
		{"hasAll: a mixed List has all of a float List", vCall(vl(vi(1), vf(1)), "hasAll", vl(vf(1))), vb(true), ""},
		{"hasAny: a List of 0.0 has -0.0", vCall(vl(vf(0)), "hasAny", vl(vf(negZero))), vb(true), ""},
		{"hasAny: a List of an int List has no List of the equal float", vCall(vl(vl(vi(1))), "hasAny", vl(vl(vf(1)))), vb(false), ""},
		{"hasAny: a List of a 0.0 List has no List of -0.0", vCall(vl(vl(vf(0))), "hasAny", vl(vl(vf(negZero)))), vb(false), ""},
		{"removeAll: a float argument leaves the equal int", vCall(vl(vi(1), vi(2)), "removeAll", vl(vf(1))), vl(vi(1), vi(2)), ""},
		{"removeAll: an int argument leaves the equal float", vCall(vl(vf(1)), "removeAll", vl(vi(1))), vl(vf(1)), ""},
		{"removeAll: an int argument removes the equal int", vCall(vl(vi(1), vi(2), vi(1)), "removeAll", vl(vi(1))), vl(vi(2)), ""},
		{"removeAll: a 0.0 argument removes -0.0", vCall(vl(vf(negZero)), "removeAll", vl(vf(0))), vl(), ""},
		{"removeAll: a float List argument leaves the equal int List", vCall(vl(vl(vi(1))), "removeAll", vl(vl(vf(1)))), vl(vl(vi(1))), ""},
		{"removeAll: List, Map and null elements by value", vCall(vl(vl(vi(1)), vm("a", vn()), vn(), vi(3)), "removeAll", vl(vl(vi(1)), vm("a", vn()), vn())), vl(vi(3)), ""},
		// Pyric bytes-literal.
		{"b'abc' in [b'abc']", vOp(vbytes("abc"), tokIn, vl(vbytes("abc"))), vb(true), ""},
	})
}

// E4: members of a set, for toSet(), in, the set methods, == between sets, and diff().
func TestEqualitySetMembers(t *testing.T) {
	nan := math.NaN()
	diffHas := func(l, r Value, kind string, key string) testExpr {
		return vOpOn(vLit(vs(key)), tokIn, vThen(vCall(l, "diff", r), kind))
	}
	runValueCases(t, []valueCase{
		// Pyric set-algebra-difference-union-intersection "setIdentity", stdlib-sets-and-mapdiff
		// "value identity"; production (Rules Test API, S0-A): sets compare numerically.
		{"a Set of an int equals a Set of the equal float", vOp(vset(vi(1)), tokEQ, vset(vf(1))), vb(true), ""},
		{"!= between them is false", vOp(vset(vi(1)), tokNE, vset(vf(1))), vb(false), ""},
		{"an int and the equal float are one element", vThen(vCall(vl(vi(1), vf(1)), "toSet"), "size"), vi(1), ""},
		{"an int is in a Set of the equal float", vOp(vi(1), tokIn, vset(vf(1))), vb(true), ""},
		{"a float is in a Set of the equal int", vOp(vf(1), tokIn, vset(vi(1))), vb(true), ""},
		{"hasAny() with a List of the equal float", vCall(vset(vi(1)), "hasAny", vl(vf(1))), vb(true), ""},
		{"hasAll() with a List of the equal float", vCall(vset(vi(1), vi(2)), "hasAll", vl(vf(1))), vb(true), ""},
		{"hasOnly() with a List of the equal float", vCall(vset(vi(1)), "hasOnly", vl(vf(1))), vb(true), ""},
		{"hasAny() with a Set of the equal float", vCall(vset(vi(1)), "hasAny", vset(vf(1))), vb(true), ""},
		{"difference() removes the equal float", vCall(vset(vi(1), vi(2)), "difference", vset(vf(1))), vset(vi(2)), ""},
		{"intersection() keeps the equal float", vThen(vCall(vset(vi(1), vi(2)), "intersection", vset(vf(1))), "size"), vi(1), ""},
		{"union() merges the equal float", vThen(vCall(vset(vi(1)), "union", vset(vf(1))), "size"), vi(1), ""},
		{"a Set of an int List equals a Set of the equal float List", vOp(vset(vl(vi(1))), tokEQ, vset(vl(vf(1)))), vb(true), ""},
		{"an int List and the equal float List are one element", vThen(vCall(vl(vl(vi(1)), vl(vf(1))), "toSet"), "size"), vi(1), ""},
		{"a Map element matches the Map of the equal float", vOp(vm("a", vi(1)), tokIn, vset(vm("a", vf(1)))), vb(true), ""},
		{"an int List is in a Set of the equal float List", vOp(vl(vi(1)), tokIn, vset(vl(vf(1)))), vb(true), ""},
		{"nested Map and List elements compare by value", vOp(vm("a", vl(vm("b", vi(2)))), tokIn, vset(vm("a", vl(vm("b", vf(2)))))), vb(true), ""},
		{"a Set of 0.0 equals a Set of -0.0", vOp(vset(vf(0)), tokEQ, vset(vf(negZero))), vb(true), ""},
		{"0.0 and -0.0 are one element", vThen(vCall(vl(vf(0), vf(negZero)), "toSet"), "size"), vi(1), ""},
		{"a Set of a 0.0 List equals a Set of a -0.0 List", vOp(vset(vl(vf(0))), tokEQ, vset(vl(vf(negZero)))), vb(true), ""},
		// NaN is one member. hidane's loose_eq (emulator) makes NaN unequal to NaN; Pyric
		// (production) finds it, and wins.
		{"two NaN are one element", vThen(vCall(vl(vf(nan), vf(nan)), "toSet"), "size"), vi(1), ""},
		{"a Set of NaN equals a Set of NaN", vOp(vset(vf(nan)), tokEQ, vset(vf(nan))), vb(true), ""},
		{"NaN is in a Set of NaN", vOp(vf(nan), tokIn, vset(vf(nan))), vb(true), ""},
		{"hasAny() finds NaN", vCall(vset(vf(nan)), "hasAny", vl(vf(nan))), vb(true), ""},
		{"two NaN Lists are one element", vThen(vCall(vl(vl(vf(nan)), vl(vf(nan))), "toSet"), "size"), vi(1), ""},
		{"sets ignore order", vOp(vset(vs("a"), vs("b")), tokEQ, vset(vs("b"), vs("a"))), vb(true), ""},
		{"a set is not its list", vOp(vset(vs("a")), tokEQ, vl(vs("a"))), vb(false), ""},
		// diff() by E4 (Pyric keyOrder and "value identity").
		{"diff() of an int and the equal float is unchanged", diffHas(vm("a", vi(1)), vm("a", vf(1)), "unchangedKeys", "a"), vb(true), ""},
		{"diff() of an int List and the equal float List is unchanged", diffHas(vm("a", vl(vi(1))), vm("a", vl(vf(1))), "unchangedKeys", "a"), vb(true), ""},
		{"diff() of 0.0 and -0.0 is unchanged", diffHas(vm("a", vf(0)), vm("a", vf(negZero)), "unchangedKeys", "a"), vb(true), ""},
		{"diff() of NaN and NaN is unchanged", diffHas(vm("a", vf(nan)), vm("a", vf(nan)), "unchangedKeys", "a"), vb(true), ""},
		{"diff() of an int and a different float is changed", diffHas(vm("a", vi(1)), vm("a", vf(1.5)), "changedKeys", "a"), vb(true), ""},
	})
}

// The index finds by the relation even past its small size, where it hashes: ints past 2^53
// share a float's bucket and stay apart from each other.
func TestIndexAgreesWithRelations(t *testing.T) {
	m := newTestMeter()
	var items []Value
	for i := range 40 {
		items = append(items, Int(int64(i)), Float(float64(i)+0.5), String(string(rune('a'+i))))
	}
	items = append(items, Int(1<<53), Int(1<<53+1), Float(negZero), List([]Value{Float(negZero)}), Float(math.NaN()))
	for _, rel := range []relation{relList, relSet} {
		ix, err := newIndex(m, rel, items)
		if err != nil {
			t.Fatal(err)
		}
		probes := append([]Value{Int(1<<53 + 1), Float(1 << 53), Float(0), List([]Value{Float(0)}),
			Float(math.NaN()), Int(77), Map(nil)}, items...)
		for _, p := range probes {
			want := false
			for _, it := range items {
				if eq, _ := equal(m, rel, it, p); eq {
					want = true
				}
			}
			got, err := ix.contains(m, p)
			if err != nil || got != want {
				t.Errorf("relation %d: index contains %s = %v, %v; comparing every item says %v", rel, showValue(p), got, err, want)
			}
		}
	}
	members, err := distinct(m, []Value{Int(1 << 53), Int(1<<53 + 1), Float(1 << 53)})
	if err != nil || len(members) != 2 {
		t.Errorf("distinct(2^53, 2^53+1, 2^53 as a float) = %d members, %v; want 2", len(members), err)
	}
}
