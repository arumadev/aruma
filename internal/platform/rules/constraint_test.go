package rules

import (
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// Sources of the cases below:
//
//	[H66]  hidane ADR 0005 (#66), the emulator's list model, as the design
//	       notes quote it: a comparison on a constraint is true when one of the
//	       field's filters alone implies it, else an error without a message, never false;
//	       n >= 1 && n <= 1 does not imply n == 1; a filter that matches nothing implies
//	       == true and == false.
//	[SIG]  the emulator's "<" takes "constraint < any" and "any < constraint"
//	       (test/oracle/rules/imported/hidane/normalised/rules-conditions-emulator.json).
//	[INSIG] "in" takes a constraint on its right: "Expected one of [list, set, map, constraint]"
//	       (test/oracle/rules/production/raw/strings.json).
//	[QRY]  production/methods.json list_query_in_request: request.query.limit <= 10 allows a
//	       query of limit 5.
//	[PROD] production, test/oracle/documents/corpus/f5-rules-constraints.json and
//	       f5-rules-queries.json, by case id.
//	[STRICT] unmeasured: the stricter answer, an error, which denies.

// listCase decides "allow list: if cond" on /c/{id} for one way whose resource.data is data.
func listCase(t *testing.T, cond string, data map[string]Value, limit *int64) bool {
	t.Helper()
	src := fmt.Sprintf(`rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /c/{id} {
      allow list: if %s;
    }
  }
}`, cond)
	return decideList(t, src, append(docPath("c"), UnknownSegment), data, limit)
}

func decideList(t *testing.T, src string, path []Segment, data map[string]Value, limit *int64) bool {
	t.Helper()
	rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: src}, DefaultLimits())
	if rs == nil {
		t.Fatalf("%s: %v", src, issues)
	}
	return decideAt(t, rs, path, data, Query(limit, 0, 0))
}

func decideAt(t *testing.T, rs *Ruleset, path []Segment, data map[string]Value, query Value) bool {
	t.Helper()
	req := request(MethodList, path, nil)
	req.Variables["resource"] = Map(map[string]Value{
		"data": PartialMap(data), "id": Unknown(), "__name__": Unknown(),
	})
	req.Variables["request"] = Map(map[string]Value{
		"auth": Null(), "method": String("list"), "path": Unknown(), "query": query,
	})
	return evaluate(t, rs, req).Allow
}

// compileList compiles "allow list: if cond" on /c/{id}, and decide decides one way of it.
func compileList(t *testing.T, cond string) *Ruleset {
	t.Helper()
	src := fmt.Sprintf(`rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /c/{id} {
      allow list: if %s;
    }
  }
}`, cond)
	rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: src}, DefaultLimits())
	if rs == nil {
		t.Fatalf("%s: %v", src, issues)
	}
	return rs
}

func decide(t *testing.T, rs *Ruleset, data map[string]Value, query Value) bool {
	t.Helper()
	return decideAt(t, rs, append(docPath("c"), UnknownSegment), data, query)
}

func n(fs ...Filter) map[string]Value { return map[string]Value{"n": Constraint(fs...)} }

func flt(op Comparator, v Value) Filter { return Filter{Op: op, Value: v} }

func strList(ss ...string) Value {
	vs := make([]Value, len(ss))
	for i, s := range ss {
		vs[i] = String(s)
	}
	return List(vs)
}

func TestConstraintComparisons(t *testing.T) {
	ts := func(sec int64) Value { return Timestamp(time.Unix(sec, 0)) }
	for _, c := range []struct {
		src  string // where the expectation comes from
		cond string
		data map[string]Value
		want bool
	}{
		// == filter.
		{"H66", "resource.data.n == 3", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n == 3.0", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n == 4", n(flt(FilterEQ, Int(3))), false},
		{"H66 never false", "!(resource.data.n == 4)", n(flt(FilterEQ, Int(3))), false},
		{"H66", "resource.data.n != 4", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n > 2", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n <= 3.0", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n < 2", n(flt(FilterEQ, Int(3))), false},
		{"SIG", "3 == resource.data.n", n(flt(FilterEQ, Int(3))), true},
		{"SIG", "2 < resource.data.n", n(flt(FilterEQ, Int(3))), true},
		{"SIG", "4 < resource.data.n", n(flt(FilterEQ, Int(3))), false},
		{"H66", "resource.data.n == 'abc'", n(flt(FilterEQ, String("abc"))), true},
		{"H66", "resource.data.n < 'abd'", n(flt(FilterEQ, String("abc"))), true},
		{"H66", "resource.data.n == true", n(flt(FilterEQ, Bool(true))), true},
		{"H66", "resource.data.n != false", n(flt(FilterEQ, Bool(true))), true},
		{"STRICT bools are not ordered", "resource.data.n > false", n(flt(FilterEQ, Bool(true))), false},
		{"STRICT cross-type", "resource.data.n == '3'", n(flt(FilterEQ, Int(3))), false},
		{"PROD a.eq-a", "resource.data.n != '3'", n(flt(FilterEQ, Int(3))), true},
		{"PROD g.eq-3-double", "resource.data.n == 3", n(flt(FilterEQ, Float(3))), true},
		{"PROD h.eq-neg-zero", "resource.data.n > -1", n(flt(FilterEQ, Float(math.Copysign(0, -1)))), true},
		{"PROD h.eq-minus-two", "resource.data.n > -1", n(flt(FilterEQ, Int(-2))), false},
		{"STRICT beyond 2^53", "resource.data.n == 9007199254740993", n(flt(FilterEQ, Int(1<<53+1))), false},
		{"STRICT containers", "resource.data.n == [1]", n(flt(FilterEQ, List([]Value{Int(1)}))), false},
		{"STRICT NaN", "resource.data.n != 1", n(flt(FilterEQ, Float(math.NaN()))), false},
		// != filter.
		{"H66", "resource.data.n != 3", n(flt(FilterNE, Int(3))), true},
		{"H66", "resource.data.n != 3.0", n(flt(FilterNE, Int(3))), true},
		{"H66", "3 != resource.data.n", n(flt(FilterNE, Int(3))), true},
		{"H66", "resource.data.n != 4", n(flt(FilterNE, Int(3))), false},
		{"H66", "resource.data.n == 4", n(flt(FilterNE, Int(3))), false},
		{"H66", "resource.data.n > 3", n(flt(FilterNE, Int(3))), false},
		{"STRICT signed zero", "resource.data.n != 0", n(flt(FilterNE, Int(0))), false},
		// < <= > >= filters.
		{"H66", "resource.data.n > 0", n(flt(FilterGT, Int(0))), true},
		{"H66", "resource.data.n > -1", n(flt(FilterGT, Int(0))), true},
		{"H66", "resource.data.n >= 0", n(flt(FilterGT, Int(0))), true},
		{"PROD c.lt-5-double, kinds", "resource.data.n > 0.5", n(flt(FilterGT, Int(1))), false},
		{"H66", "resource.data.n > 1", n(flt(FilterGT, Int(0))), false},
		{"H66", "resource.data.n < 100", n(flt(FilterGT, Int(0))), false},
		{"H66", "resource.data.n != -5", n(flt(FilterGT, Int(0))), true},
		{"H66", "resource.data.n != 0", n(flt(FilterGT, Int(0))), true},
		{"H66", "resource.data.n > 1", n(flt(FilterGE, Int(1))), false},
		{"H66", "resource.data.n >= 1", n(flt(FilterGE, Int(1))), true},
		{"H66", "resource.data.n > 0", n(flt(FilterGE, Int(1))), true},
		{"PROD c.lt-10", "resource.data.n < 10", n(flt(FilterLT, Int(10))), true},
		{"PROD c.lt-5", "resource.data.n < 10", n(flt(FilterLT, Int(5))), true},
		{"PROD c.lte-9", "resource.data.n < 10", n(flt(FilterLE, Int(9))), true},
		{"PROD c.lte-10", "resource.data.n < 10", n(flt(FilterLE, Int(10))), false},
		{"PROD c.lt-5-double", "resource.data.n < 10", n(flt(FilterLT, Float(5))), false},
		{"STRICT int bound against a float", "resource.data.n < 10.0", n(flt(FilterLT, Int(5))), false},
		{"PROD d.gte-0", "resource.data.n > 0", n(flt(FilterGE, Int(0))), false},
		{"H66", "resource.data.n <= 10", n(flt(FilterLE, Int(10))), true},
		{"H66", "resource.data.n != 10", n(flt(FilterLT, Int(10))), true},
		{"H66", "resource.data.n != 9", n(flt(FilterLT, Int(10))), false},
		{"H66", "resource.data.n < 'z'", n(flt(FilterLT, String("m"))), true},
		{"H66", "resource.data.n < 'm'", n(flt(FilterLT, String("m"))), true},
		{"H66", "resource.data.n <= 'm'", n(flt(FilterLE, String("m"))), true},
		{"H66", "resource.data.n < 'm'", n(flt(FilterLE, String("m"))), false},
		{"H66", "resource.data.n > 'a'", n(flt(FilterGT, String("m"))), true},
		{"H66", "resource.data.n > 'z'", n(flt(FilterGT, String("m"))), false},
		{"STRICT UTF-16 against UTF-8 order", "resource.data.n > ''", n(flt(FilterGT, String(""))), false},
		{"H66", "resource.data.n > b'\\x00'", n(flt(FilterGT, Bytes([]byte{1}))), true},
		{"H66", "resource.data.n > timestamp.value(0)", n(flt(FilterGE, ts(10))), true},
		{"H66", "resource.data.n < timestamp.value(0)", n(flt(FilterGE, ts(10))), false},
		{"STRICT cross-type", "resource.data.n > 'a'", n(flt(FilterGT, Int(0))), false},
		// A filter that matches nothing.
		{"H66", "resource.data.n == true", n(flt(FilterGT, Bool(true))), true},
		{"H66", "resource.data.n == false", n(flt(FilterGT, Bool(true))), true},
		{"H66", "resource.data.n == 'x'", n(flt(FilterLT, Bool(false))), true},
		{"H66", "resource.data.n < 0", n(flt(FilterLT, String(""))), true},
		// in and array-contains-any, split into ways by the binding; unsplit they imply nothing.
		{"H66", "resource.data.n == 'a'", n(flt(FilterIn, strList("a", "b"))), false},
		{"H66", "resource.data.n == 'a'", n(flt(FilterEQ, String("a"))), true},
		{"PROD b.eq-a", "resource.data.n in ['a', 'b']", n(flt(FilterEQ, String("a"))), true},
		{"PROD q10.eq-3", "resource.data.n in [1, 2]", n(flt(FilterEQ, Int(3))), false},
		{"STRICT E3 kind", "resource.data.n in [1.0, 2.0]", n(flt(FilterEQ, Int(1))), false},
		{"E4", "resource.data.n in [1.0, 2.0].toSet()", n(flt(FilterEQ, Int(1))), true},
		{"STRICT", "resource.data.n in [1, 2]", n(flt(FilterGE, Int(1)), flt(FilterLE, Int(2))), false},
		{"STRICT", "resource.data.n in [1, 2]", n(flt(FilterNotIn, List([]Value{Int(3)}))), false},
		{"STRICT", "resource.data.n in {'a': 1}", n(flt(FilterEQ, String("a"))), false},
		// not-in.
		{"H66", "resource.data.n != 'a'", n(flt(FilterNotIn, strList("a", "b"))), true},
		{"H66", "resource.data.n != 'b'", n(flt(FilterNotIn, strList("a", "b"))), true},
		{"H66", "resource.data.n != 'c'", n(flt(FilterNotIn, strList("a", "b"))), false},
		{"H66", "resource.data.n == 'c'", n(flt(FilterNotIn, strList("a", "b"))), false},
		// array-contains.
		{"INSIG", "'a' in resource.data.n", n(flt(FilterContains, String("a"))), true},
		{"INSIG", "'b' in resource.data.n", n(flt(FilterContains, String("a"))), false},
		{"STRICT E3 int against float", "1 in resource.data.n", n(flt(FilterContains, Int(1))), false},
		{"STRICT", "resource.data.n in ['a']", n(flt(FilterContains, String("a"))), false},
		{"STRICT", "resource.data.n.hasAny(['a'])", n(flt(FilterContains, String("a"))), false},
		{"STRICT", "resource.data.n.size() > 0", n(flt(FilterContains, String("a"))), false},
		{"STRICT", "'a' in resource.data.n", n(flt(FilterContainsAny, strList("a", "b"))), false},
		{"H66", "'b' in resource.data.n", n(flt(FilterContains, String("b"))), true},
		// Several filters on one field: one alone must imply.
		{"H66", "resource.data.n == 1", n(flt(FilterGE, Int(1)), flt(FilterLE, Int(1))), false},
		{"H66", "resource.data.n >= 1", n(flt(FilterGE, Int(1)), flt(FilterLE, Int(1))), true},
		{"H66", "resource.data.n != 5", n(flt(FilterGT, Int(0)), flt(FilterNE, Int(5))), true},
		{"H66", "resource.data.n > 0 && resource.data.n != 5", n(flt(FilterGT, Int(0)), flt(FilterNE, Int(5))), true},
		{"PROD d.range-1-5", "resource.data.n > 0 && resource.data.n < 9", n(flt(FilterGT, Int(0)), flt(FilterLT, Int(9))), true},
		// null.
		{"H66", "resource.data.n == null", n(flt(FilterEQ, Null())), true},
		{"H66", "resource.data.n != null", n(flt(FilterNE, Null())), true},
		{"PROD a.eq-a", "resource.data.n != null", n(flt(FilterEQ, String("a"))), true},
		{"STRICT", "resource.data.n < null", n(flt(FilterEQ, Null())), false},
		// Errors absorbed as everywhere else.
		{"H66", "resource.data.n == 4 || true", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n == 4 || resource.data.n == 3", n(flt(FilterEQ, Int(3))), true},
		{"H66", "resource.data.n == 4 && false", n(flt(FilterEQ, Int(3))), false},
		{"H66", "resource.data.n == 3 ? true : false", n(flt(FilterEQ, Int(3))), true},
		// Everything else on a Constraint, an Unknown or a PartialMap is an error.
		{"STRICT", "resource.data.n is int", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.n is constraint", n(flt(FilterEQ, Int(3))), false},
		{"PROD q10.eq-1", "resource.data.n in [3]", n(flt(FilterEQ, Int(3))), true},
		{"STRICT", "[resource.data.n] != [4]", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "{'k': resource.data.n} != {'k': 4}", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "int(resource.data.n) == 3", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "string(resource.data.n) == '3'", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "-resource.data.n < 0", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "!resource.data.n", n(flt(FilterEQ, Bool(false))), false},
		{"STRICT", "resource.data.n", n(flt(FilterEQ, Bool(true))), false},
		{"STRICT", "resource.data.n + 1 > 3", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.n == resource.data.n", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.size() == 1", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.keys().size() == 1", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "!('x' in resource.data)", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data != {}", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data is map", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.get('n', 0) == 3", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.data.other == null", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.values().size() == 3", n(flt(FilterEQ, Int(3))), true}, // resource's keys are known
		{"STRICT", "resource.id == 'x'", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "resource.id != 'x'", n(flt(FilterEQ, Int(3))), false},
		{"STRICT", "request.path != null", n(flt(FilterEQ, Int(3))), false},
		{"H66", "resource['data']['n'] == 3", n(flt(FilterEQ, Int(3))), true},
		// A field a value: every document the way matches holds it.
		{"binding", "resource.data.n is int", map[string]Value{"n": Int(3)}, true},
	} {
		if got := listCase(t, c.cond, c.data, nil); got != c.want {
			t.Errorf("[%s] %s with %v: allow %v, want %v", c.src, c.cond, filtersOf(c.data), got, c.want)
		}
	}
}

func filtersOf(data map[string]Value) any {
	if c, ok := data["n"]; ok && c.kind == KindConstraint {
		return c.filters()
	}
	return data
}

// Map fields: a filter on a.b makes resource.data.a a PartialMap holding b.
func TestConstraintNestedFields(t *testing.T) {
	data := map[string]Value{"a": PartialMap(map[string]Value{"b": Constraint(flt(FilterGT, Int(0)))})}
	for cond, want := range map[string]bool{
		"resource.data.a.b > 0":           true,
		"resource.data['a']['b'] > 0":     true,
		"resource.data.a['b'] >= 0":       true,
		"resource.data.a.b > 1":           false,
		"resource.data.a.c > 0":           false,
		"resource.data.a.size() == 1":     false,
		"resource.data.a != {}":           false,
		"'b' in resource.data.a":          false,
		"resource.data.a.keys() == ['b']": false,
		"resource.data.a.b.c > 0":         false,
	} {
		if got := listCase(t, cond, data, nil); got != want {
			t.Errorf("%s: allow %v, want %v", cond, got, want)
		}
	}
}

// A function receives a Constraint as it receives any value, and compares it the same way.
func TestConstraintThroughFunctions(t *testing.T) {
	src := `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    function owner(d) { let o = d.owner; return o == 'alice'; }
    function big(x) { return x.size() > 1; }
    match /c/{id} {
      allow list: if owner(resource.data);
    }
    match /d/{id} {
      allow list: if big(resource.data.owner);
    }
  }
}`
	data := map[string]Value{"owner": Constraint(flt(FilterEQ, String("alice")))}
	if !decideList(t, src, append(docPath("c"), UnknownSegment), data, nil) {
		t.Error("owner(resource.data): denied")
	}
	if decideList(t, src, append(docPath("d"), UnknownSegment), data, nil) {
		t.Error("big(resource.data.owner): allowed")
	}
}

func TestConstraintQuery(t *testing.T) {
	five := int64(5)
	// [QRY] recorded in production.
	if !listCase(t, "request.query.limit <= 10", nil, &five) {
		t.Error("limit 5 <= 10: denied")
	}
	// [PROD] q2, q3, q4 to q7, q12, q13: no limit is null, no offset 0, orderBy a list of
	// unknown orders.
	for cond, want := range map[string]bool{
		"request.query.limit <= 10":         false,
		"request.query.limit == null":       true,
		"request.query.offset <= 1":         true,
		"'orderBy' in request.query":        true,
		"request.query.orderBy.size() == 0": true,
		"request.query.size() == 3":         true,
	} {
		if got := listCase(t, cond, nil, nil); got != want {
			t.Errorf("%s without a limit: allow %v, want %v", cond, got, want)
		}
	}
	rs := compileList(t, "request.query.orderBy[0] == 'x' || request.query.orderBy.x == 'ASCENDING' || 'x' in request.query.orderBy")
	if decide(t, rs, nil, Query(nil, 0, 1)) {
		t.Error("orderBy's elements are known")
	}
	rs = compileList(t, "request.query.orderBy.size() == 1")
	if !decide(t, rs, nil, Query(nil, 0, 1)) {
		t.Error("orderBy.size() == 1 with one order: denied")
	}
}

// Path variables that took an unknown segment have no value (ADR-021 §2.4 item 2).
func TestConstraintUnknownSegments(t *testing.T) {
	src := `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /{rest=**}/c/{id} {
      allow list: if %s;
    }
  }
}`
	group := append(append(docPath(), UnknownRun), Literal("c"), UnknownSegment)
	for cond, want := range map[string]bool{
		"true":                    true,
		"id == 'x'":               false,
		"id != 'x'":               false,
		"rest != null":            false,
		"database == '(default)'": true,
	} {
		if got := decideList(t, fmt.Sprintf(src, cond), group, nil, nil); got != want {
			t.Errorf("%s: allow %v, want %v", cond, got, want)
		}
	}
}

// The property the model rests on: when a Constraint makes a comparison true, so does every
// document the filters match, as Firestore matches it. Firestore's matching is modelled with
// every doubt resolved against the engine: NaN sorts before every number, -0.0 is taken both
// as 0.0 and as a value below it, and != keeps null.
func TestConstraintNeverAllowsMore(t *testing.T) {
	pool := constraintPool()
	m := constraintMeter()
	ops := []token{tokEQ, tokNE, tokLT, tokLE, tokGT, tokGE, tokIn, tokInList}
	comps := []Comparator{FilterEQ, FilterNE, FilterLT, FilterLE, FilterGT, FilterGE, FilterNotIn,
		FilterContains, FilterIn, FilterContainsAny}
	check := func(fs []Filter, op token, b Value) {
		var got Value
		var err error
		switch op {
		case tokIn:
			got, err = constrained(m, tokIn, b, Constraint(fs...))
		case tokInList:
			got, err = constrained(m, tokIn, Constraint(fs...), b)
		default:
			got, err = constrained(m, op, Constraint(fs...), b)
		}
		if err != nil || !got.boolean() {
			return
		}
		for _, v := range pool {
			if !matchesAll(v, fs) || op == tokInList && !ownKind(v, fs) {
				continue
			}
			var r Value
			var rerr error
			switch op {
			case tokIn:
				r, rerr = binary(m, tokIn, b, v)
			case tokInList:
				r, rerr = binary(m, tokIn, v, b)
			default:
				r, rerr = binary(m, op, v, b)
			}
			if rerr != nil || r.kind != KindBool || !r.boolean() {
				t.Fatalf("filters %v imply v %s %v, but the document %v gives %v, %v", fs, op, b, v, r, rerr)
			}
		}
	}
	filterValues := append(slicesOfPool(pool), strList("a", "b"), List([]Value{Int(0), Float(1.5)}))
	pool = append(pool, setOf([]Value{Int(1), String("a")}), setOf([]Value{Float(0), Float(3)}))
	for _, fop := range comps {
		for _, a := range filterValues {
			for _, op := range ops {
				for _, b := range pool {
					check([]Filter{{fop, a}}, op, b)
				}
			}
		}
	}
	r := rand.New(rand.NewPCG(5, 1))
	for range 20000 {
		k := 1 + r.IntN(3)
		fs := make([]Filter, k)
		for i := range fs {
			fs[i] = Filter{comps[r.IntN(len(comps))], filterValues[r.IntN(len(filterValues))]}
		}
		check(fs, ops[r.IntN(len(ops))], pool[r.IntN(len(pool))])
	}
}

func FuzzConstraint(f *testing.F) {
	f.Add(uint8(4), 3, uint8(4), 0)
	f.Add(uint8(0), 7, uint8(1), 9)
	pool := constraintPool()
	m := constraintMeter()
	f.Fuzz(func(t *testing.T, fop uint8, ai int, op uint8, bi int) {
		ops := []token{tokEQ, tokNE, tokLT, tokLE, tokGT, tokGE, tokIn}
		a, b := pool[abs(ai)%len(pool)], pool[abs(bi)%len(pool)]
		fs := []Filter{{Comparator(1 + fop%10), a}}
		o := ops[int(op)%len(ops)]
		got, err := constrained(m, o, Constraint(fs...), b)
		if o == tokIn {
			got, err = constrained(m, o, b, Constraint(fs...))
		}
		if err != nil {
			if err.Error() != "" {
				t.Fatalf("error with a message: %v", err)
			}
			return
		}
		for _, v := range pool {
			if !matchesAll(v, fs) {
				continue
			}
			l, r := v, b
			if o == tokIn {
				l, r = b, v
			}
			res, rerr := binary(m, o, l, r)
			if got.boolean() && (rerr != nil || !res.boolean()) {
				t.Fatalf("%v implies %s %v, the document %v denies", fs, o, b, v)
			}
		}
	})
}

func abs(i int) int {
	if i < 0 {
		return -(i + 1)
	}
	return i
}

func constraintMeter() *meter {
	lim := DefaultLimits()
	return newMeter(&lim, NewInflight(1<<30), nil)
}

func slicesOfPool(pool []Value) []Value { return append([]Value{}, pool...) }

func constraintPool() []Value {
	big := int64(1) << 53
	pool := []Value{Null(), Bool(false), Bool(true)}
	for _, i := range []int64{math.MinInt64, -big - 1, -big, -big + 1, -1, 0, 1, 2, 3, 10, big - 1, big, big + 1, big + 3, math.MaxInt64} {
		pool = append(pool, Int(i))
	}
	for _, x := range []float64{math.Inf(-1), -1.5, math.Copysign(0, -1), 0, 0.5, 1, 2.5, 3, float64(big - 1), float64(big), float64(big + 2), float64(big + 4), 1e300, math.Inf(1), math.NaN()} {
		pool = append(pool, Float(x))
	}
	for _, s := range []string{"", "a", "ab", "b", "m", "z", "é", "", "￿", "\U00010000", "\U0001F600"} {
		pool = append(pool, String(s))
	}
	for _, b := range []string{"", "\x00", "\x01", "\xff"} {
		pool = append(pool, Bytes([]byte(b)))
	}
	for _, s := range []int64{-1, 0, 10, 1 << 40} {
		pool = append(pool, Timestamp(time.Unix(s, 0)))
	}
	pool = append(pool, List([]Value{Int(1)}), List([]Value{Float(1)}), List([]Value{String("a")}),
		List([]Value{String("a"), String("b")}), List([]Value{Null()}), List([]Value{Bool(true)}), List(nil),
		Map(map[string]Value{"k": Int(1)}), Map(nil), LatLng(1, 2), PathOf([]string{"c", "d"}))
	return pool
}

// ownKind reports whether v, a document a field equality matches, is of the kind of that
// equality's value when both are numbers: production allows x == 1 for a rule x in [1, 2]
// (q10.eq-1), though the rules' in finds no 1.0 there; the property holds for the rest.
func ownKind(v Value, fs []Filter) bool {
	if !v.isNumber() {
		return true
	}
	return slices.ContainsFunc(fs, func(f Filter) bool { return f.Op == FilterEQ && f.Value.kind == v.kind })
}

// The Firestore side of the property: whether the document field v matches every filter.

func matchesAll(v Value, fs []Filter) bool {
	for _, f := range fs {
		if !fsMatches(v, f) {
			return false
		}
	}
	return true
}

func fsMatches(v Value, f Filter) bool {
	// -0.0 is taken both ways: as 0.0, and as a value just below it.
	if v.kind == KindFloat && v.float() == 0 && math.Signbit(v.float()) {
		return fsMatch(v, f, false) || fsMatch(v, f, true)
	}
	if f.Value.kind == KindFloat && f.Value.float() == 0 && math.Signbit(f.Value.float()) {
		return fsMatch(v, f, false) || fsMatch(v, f, true)
	}
	return fsMatch(v, f, false)
}

func fsMatch(v Value, f Filter, signedZero bool) bool {
	a := f.Value
	switch f.Op {
	case FilterEQ:
		return fsCompare(v, a, signedZero) == 0
	case FilterNE:
		return fsCompare(v, a, signedZero) != 0
	case FilterNotIn:
		for _, e := range a.items() {
			if fsCompare(v, e, signedZero) == 0 {
				return false
			}
		}
		return true
	case FilterIn:
		for _, e := range a.items() {
			if fsCompare(v, e, signedZero) == 0 {
				return true
			}
		}
		return false
	case FilterContains, FilterContainsAny:
		if v.kind != KindList {
			return false
		}
		want := []Value{a}
		if f.Op == FilterContainsAny {
			want = a.items()
		}
		for _, e := range v.items() {
			for _, w := range want {
				if fsCompare(e, w, signedZero) == 0 {
					return true
				}
			}
		}
		return false
	}
	if fsType(v) != fsType(a) || isNaN(v) || isNaN(a) {
		return false // inequalities match their own type only, and never NaN (value.InRange)
	}
	c := fsCompare(v, a, signedZero)
	switch f.Op {
	case FilterLT:
		return c < 0
	case FilterLE:
		return c <= 0
	case FilterGT:
		return c > 0
	}
	return c >= 0
}

// fsType is Firestore's type order.
func fsType(v Value) int {
	switch v.kind {
	case KindNull:
		return 0
	case KindBool:
		return 1
	case KindInt, KindFloat:
		return 2
	case KindTimestamp:
		return 3
	case KindString:
		return 4
	case KindBytes:
		return 5
	case KindPath:
		return 6
	case KindLatLng:
		return 7
	case KindList:
		return 8
	}
	return 9
}

// fsCompare orders two values as Firestore does: numbers by exact value with NaN first
// (and, with signedZero, -0.0 below 0.0), strings by UTF-8 bytes.
func fsCompare(x, y Value, signedZero bool) int {
	if tx, ty := fsType(x), fsType(y); tx != ty {
		return tx - ty
	}
	switch x.kind {
	case KindNull:
		return 0
	case KindBool:
		return compareInts(boolInt(x.boolean()), boolInt(y.boolean()))
	case KindTimestamp:
		return x.instant().Compare(y.instant())
	case KindString, KindBytes:
		return strings.Compare(x.str(), y.str())
	case KindLatLng:
		px, py := x.point(), y.point()
		if px == py {
			return 0
		}
		return 1
	case KindPath:
		return strings.Compare(strings.Join(x.segments(), "/"), strings.Join(y.segments(), "/"))
	case KindList:
		xs, ys := x.items(), y.items()
		for i := range min(len(xs), len(ys)) {
			if c := fsCompare(xs[i], ys[i], signedZero); c != 0 {
				return c
			}
		}
		return len(xs) - len(ys)
	case KindMap:
		if fmt.Sprint(x.mapv().keys) == fmt.Sprint(y.mapv().keys) {
			for _, k := range x.mapv().keys {
				if c := fsCompare(x.mapv().entries[k], y.mapv().entries[k], signedZero); c != 0 {
					return c
				}
			}
			return 0
		}
		return 1
	}
	return fsNumbers(x, y, signedZero)
}

func fsNumbers(x, y Value, signedZero bool) int {
	nx, ny := x.kind == KindFloat && math.IsNaN(x.float()), y.kind == KindFloat && math.IsNaN(y.float())
	switch {
	case nx && ny:
		return 0
	case nx:
		return -1
	case ny:
		return 1
	}
	if x.kind == KindInt && y.kind == KindInt {
		return compareInts(x.integer(), y.integer())
	}
	if signedZero && x.kind == KindFloat && y.kind == KindFloat && x.float() == 0 && y.float() == 0 {
		return compareInts(boolInt(!math.Signbit(x.float())), boolInt(!math.Signbit(y.float())))
	}
	bx, by := exact(x), exact(y)
	if c := bx.Cmp(by); c != 0 || !signedZero {
		return c
	}
	// Equal and zero: a -0.0 against an int 0 lies below it.
	sx := x.kind == KindFloat && math.Signbit(x.float())
	sy := y.kind == KindFloat && math.Signbit(y.float())
	return compareInts(boolInt(!sx), boolInt(!sy))
}

func isNaN(v Value) bool { return v.kind == KindFloat && math.IsNaN(v.float()) }

func exact(v Value) *big.Float {
	if v.kind == KindInt {
		return new(big.Float).SetInt64(v.integer())
	}
	return big.NewFloat(v.float())
}
