package query

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

const root = "projects/p/databases/(default)/documents"

// docs are the candidates of the tests, in name order: one per kind of value under f, and one
// without f.
var docs = []struct {
	id     string
	fields string
}{
	{"a", `{"f":{"nullValue":null},"n":{"integerValue":"1"}}`},
	{"b", `{"f":{"doubleValue":"NaN"},"n":{"integerValue":"2"}}`},
	{"c", `{"f":{"integerValue":"1"},"n":{"integerValue":"3"}}`},
	{"d", `{"f":{"doubleValue":1.5},"n":{"integerValue":"4"}}`},
	{"e", `{"f":{"stringValue":"x"},"n":{"integerValue":"5"}}`},
	{"f", `{"f":{"arrayValue":{"values":[{"integerValue":"1"},{"stringValue":"x"}]}},"n":{"integerValue":"6"}}`},
	{"g", `{"f":{"arrayValue":{"values":[{"arrayValue":{}}]}},"n":{"integerValue":"7"}}`},
	{"h", `{"f":{"arrayValue":{}},"n":{"integerValue":"8"}}`},
	{"i", `{"n":{"integerValue":"9"}}`},
	{"j", `{"f":{"booleanValue":true},"n":{"integerValue":"10"}}`},
}

func candidates(t *testing.T) []*Doc {
	t.Helper()
	var out []*Doc
	for _, d := range docs {
		m := &pb.MapValue{}
		if err := protojson.Unmarshal([]byte(`{"fields":`+d.fields+`}`), m); err != nil {
			t.Fatal(err)
		}
		v, err := wire.MapFromProto(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, &Doc{Name: root + "/c/" + d.id, Fields: v})
	}
	return out
}

func parse(t *testing.T, sq string) (*Query, error) {
	t.Helper()
	m := &pb.StructuredQuery{}
	if err := protojson.Unmarshal([]byte(sq), m); err != nil {
		t.Fatalf("%s: %v", sq, err)
	}
	return Parse(m, wire.Name{Database: wire.Database{Project: "p", ID: "(default)"}})
}

// run is the ids q returns over candidates, in order, and the offset's skipped count.
func run(t *testing.T, sq string) ([]string, int) {
	t.Helper()
	q, err := parse(t, sq)
	if err != nil {
		t.Fatalf("%s: %v", sq, err)
	}
	var ids []string
	r := NewRunner(q, false, Limits{}, func(d *Doc) error {
		ids = append(ids, d.Name[strings.LastIndexByte(d.Name, '/')+1:])
		return nil
	})
	for _, d := range candidates(t) {
		done, err := r.Add(d)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	return ids, r.Skipped()
}

func where(f string) string { return `{"from":[{"collectionId":"c"}],"where":` + f + `}` }

func field(op, v string) string {
	return `{"fieldFilter":{"field":{"fieldPath":"f"},"op":"` + op + `","value":` + v + `}}`
}

func unary(op string) string { return `{"unaryFilter":{"field":{"fieldPath":"f"},"op":"` + op + `"}}` }

// Each operator over every kind of value: null and NaN match only IS_NULL and IS_NAN, a range
// stays in its operand's type class, a missing field matches nothing, != and not-in leave out
// null, and the inequalities order by f implicitly; array-contains null matches the array
// holding an empty array.
func TestOperators(t *testing.T) {
	for _, c := range []struct {
		filter string
		want   string
	}{
		{field("EQUAL", `{"integerValue":"1"}`), "c"},
		{field("EQUAL", `{"doubleValue":1}`), "c"},
		{field("EQUAL", `{"nullValue":null}`), ""},
		{field("EQUAL", `{"doubleValue":"NaN"}`), ""},
		{field("NOT_EQUAL", `{"integerValue":"1"}`), "j b d e h f g"},
		{field("NOT_EQUAL", `{"nullValue":null}`), ""},
		{field("LESS_THAN", `{"integerValue":"2"}`), "c d"},
		{field("GREATER_THAN_OR_EQUAL", `{"integerValue":"1"}`), "c d"},
		{field("GREATER_THAN", `{"stringValue":""}`), "e"},
		{field("LESS_THAN", `{"booleanValue":true}`), ""},
		{field("IN", `{"arrayValue":{"values":[{"integerValue":"1"},{"stringValue":"x"},{"nullValue":null}]}}`), "c e"},
		{field("NOT_IN", `{"arrayValue":{"values":[{"integerValue":"1"},{"stringValue":"x"}]}}`), "j b d h f g"},
		{field("NOT_IN", `{"arrayValue":{"values":[{"nullValue":null}]}}`), ""},
		{field("ARRAY_CONTAINS", `{"stringValue":"x"}`), "f"},
		{field("ARRAY_CONTAINS", `{"nullValue":null}`), "g"},
		{field("ARRAY_CONTAINS_ANY", `{"arrayValue":{"values":[{"integerValue":"1"},{"arrayValue":{}}]}}`), "f g"},
		{unary("IS_NULL"), "a"},
		{unary("IS_NOT_NULL"), "j b c d e h f g"},
		{unary("IS_NAN"), "b"},
		{unary("IS_NOT_NAN"), "j c d e h f g"},
		{`{"compositeFilter":{"op":"OR","filters":[` + unary("IS_NULL") + `,` + field("EQUAL", `{"booleanValue":true}`) + `]}}`, "a j"},
		{`{"compositeFilter":{"op":"AND","filters":[` + field("GREATER_THAN", `{"integerValue":"0"}`) + `,` +
			`{"fieldFilter":{"field":{"fieldPath":"n"},"op":"LESS_THAN","value":{"integerValue":"4"}}}]}}`, "c"},
	} {
		ids, _ := run(t, where(c.filter))
		if got := strings.Join(ids, " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.filter, got, c.want)
		}
	}
}

// An order on f leaves out the documents without f and sorts across types by Firestore's
// order: null, booleans, NaN and the numbers, strings, arrays by their elements.
func TestOrderAcrossTypes(t *testing.T) {
	ids, _ := run(t, `{"from":[{"collectionId":"c"}],"orderBy":[{"field":{"fieldPath":"f"}}]}`)
	if got := strings.Join(ids, " "); got != "a j b c d e h f g" {
		t.Errorf("order by f: %q", got)
	}
	ids, _ = run(t, `{"from":[{"collectionId":"c"}],"orderBy":[{"field":{"fieldPath":"f"},"direction":"DESCENDING"}],"limit":3}`)
	if got := strings.Join(ids, " "); got != "g f h" {
		t.Errorf("order by f desc limit 3: %q", got)
	}
}

// An inequality orders by its field implicitly, in the last explicit order's direction, then by
// __name__ in that direction.
func TestImplicitOrder(t *testing.T) {
	q, err := parse(t, `{"from":[{"collectionId":"c"}],"where":`+
		`{"fieldFilter":{"field":{"fieldPath":"n"},"op":"GREATER_THAN","value":{"integerValue":"3"}}},`+
		`"orderBy":[{"field":{"fieldPath":"f"},"direction":"DESCENDING"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range q.Order {
		got = append(got, o.Path.String()+map[bool]string{true: " desc", false: ""}[o.Desc])
	}
	if want := []string{"f desc", "n desc", "__name__ desc"}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
	if _, err := parse(t, `{"from":[{"collectionId":"c"}],"where":`+
		`{"fieldFilter":{"field":{"fieldPath":"n"},"op":"GREATER_THAN","value":{"integerValue":"3"}}},`+
		`"orderBy":[{"field":{"fieldPath":"__name__"}}]}`); !errors.Is(err, errAfterKey) {
		t.Errorf("an inequality after an explicit __name__: %v", err)
	}
}

// Cursors: before or after their values, a prefix of the order, __name__ as a reference;
// offset and limit after them.
func TestCursors(t *testing.T) {
	order := `"orderBy":[{"field":{"fieldPath":"n"}}]`
	for _, c := range []struct {
		cursor string
		want   string
	}{
		{`"startAt":{"values":[{"integerValue":"3"}],"before":true}`, "c d e f g h i j"},
		{`"startAt":{"values":[{"integerValue":"3"}]}`, "d e f g h i j"},
		{`"endAt":{"values":[{"integerValue":"3"}],"before":true}`, "a b"},
		{`"endAt":{"values":[{"integerValue":"3"}]}`, "a b c"},
		{`"startAt":{"values":[{"integerValue":"3"},{"referenceValue":"` + root + `/c/c"}]}`, "d e f g h i j"},
		{`"startAt":{"values":[{"integerValue":"3"}],"before":true},"offset":2,"limit":3`, "e f g"},
	} {
		// A cursor holds a value per explicit order at most: __name__ is ordered explicitly.
		o := order
		if strings.Contains(c.cursor, "referenceValue") {
			o = `"orderBy":[{"field":{"fieldPath":"n"}},{"field":{"fieldPath":"__name__"}}]`
		}
		ids, _ := run(t, `{"from":[{"collectionId":"c"}],`+o+`,`+c.cursor+`}`)
		if got := strings.Join(ids, " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.cursor, got, c.want)
		}
	}
	// Ordered by __name__ alone the results stream; the offset counts what it skips.
	ids, skipped := run(t, `{"from":[{"collectionId":"c"}],"orderBy":[{"field":{"fieldPath":"__name__"}}],`+
		`"startAt":{"values":[{"referenceValue":"`+root+`/c/b"}],"before":true},"offset":2,"limit":2}`)
	if got := strings.Join(ids, " "); got != "d e" || skipped != 2 {
		t.Errorf("by name: %q skipped %d", got, skipped)
	}
	for _, sq := range []string{
		`{"from":[{"collectionId":"c"}],"startAt":{"values":[{"integerValue":"1"},{"integerValue":"1"}]}}`,
		`{"from":[{"collectionId":"c"}],"startAt":{"values":[{"integerValue":"1"}]}}`,
	} {
		if _, err := parse(t, sq); err == nil {
			t.Errorf("%s: no error", sq)
		}
	}
}

// The limits and their recorded texts (f0-errors.json).
func TestLimits(t *testing.T) {
	values := func(n int) string {
		var vs []string
		for i := range n {
			vs = append(vs, `{"integerValue":"`+string(rune('0'+i%10))+`"}`)
		}
		return `{"arrayValue":{"values":[` + strings.Join(vs, ",") + `]}}`
	}
	or := func(n int) string {
		var fs []string
		for range n {
			fs = append(fs, field("EQUAL", `{"integerValue":"1"}`))
		}
		return `{"compositeFilter":{"op":"OR","filters":[` + strings.Join(fs, ",") + `]}}`
	}
	for _, c := range []struct {
		filter, want string
	}{
		{field("IN", values(30)), ""},
		{field("IN", values(31)), "'IN' supports up to 30 comparison values."},
		{field("NOT_IN", values(10)), ""},
		{field("NOT_IN", values(11)), "'NOT_IN' supports up to 10 comparison values."},
		{or(30), ""},
		{or(31), "Too many disjunctions after normalization. Result had 31 disjunctions which is more than the maximum of 30"},
		{`{"compositeFilter":{"op":"AND","filters":[` + field("IN", values(6)) + `,` +
			`{"fieldFilter":{"field":{"fieldPath":"g"},"op":"IN","value":` + values(6) + `}}]}}`,
			"Too many disjunctions after normalization. Result had 36 disjunctions which is more than the maximum of 30"},
		{field("IN", `{"arrayValue":{}}`), "'IN' requires an non-empty ArrayValue."},
		{`{"compositeFilter":{"op":"AND","filters":[` + field("NOT_IN", values(1)) + `,` + field("NOT_EQUAL", `{"integerValue":"1"}`) + `]}}`,
			wire.From(errTwoNotEqual).Message},
	} {
		_, err := parse(t, where(c.filter))
		got := ""
		if err != nil {
			got = wire.From(err).Message
		}
		if got != c.want {
			t.Errorf("%.80s: %q, want %q", c.filter, got, c.want)
		}
	}
}

// Aggregations: a document without a sum or avg field counts toward none of them; a sum stays
// an integer until it overflows or meets a double; avg is a double, null over nothing; count
// stops at up_to.
func TestAggregator(t *testing.T) {
	n, _ := value.ParseFieldPath("n")
	f, _ := value.ParseFieldPath("f")
	a := NewAggregator([]Aggregation{{Op: index.AggCount, Alias: "count"}, {Op: index.AggSum, Field: n, Alias: "sum"}, {Op: index.AggAvg, Field: n, Alias: "avg"}})
	for _, d := range candidates(t) {
		a.Add(d)
	}
	r := a.Result()
	if !value.Equal(r["count"], value.Int(10)) || !value.Equal(r["sum"], value.Int(55)) || !value.Equal(r["avg"], value.Double(5.5)) {
		t.Errorf("count, sum, avg of n: %v", r)
	}
	// Over f only the numbers count, NaN among them.
	a = NewAggregator([]Aggregation{{Op: index.AggSum, Field: f, Alias: "s"}})
	for _, d := range candidates(t) {
		a.Add(d)
	}
	if s := a.Result()["s"]; !s.IsNaN() {
		t.Errorf("sum of f: %v, want NaN", s)
	}
	a = NewAggregator([]Aggregation{{Op: index.AggAvg, Field: f, Alias: "v"}})
	if v := a.Result()["v"]; !v.IsNull() {
		t.Errorf("avg over nothing: %v", v)
	}
	big := &Doc{Name: root + "/c/x", Fields: value.MapOf(map[string]value.Value{"n": value.Int(math.MaxInt64)})}
	a = NewAggregator([]Aggregation{{Op: index.AggSum, Field: n, Alias: "s"}})
	a.Add(big)
	a.Add(big)
	if s := a.Result()["s"]; s.Kind() != value.KindDouble {
		t.Errorf("an overflowing sum: %v, want a double", s)
	}
	// A sum is exact in any order (f3-aggregations.json set.dbl, set.ovf-back): the index paths
	// and the reference path read the documents in different orders and agree.
	for _, set := range []struct {
		vals     []value.Value
		sum, avg value.Value
	}{
		{[]value.Value{value.Double(0.1), value.Double(0.2), value.Double(0.3)}, value.Double(0.6), value.Double(0.19999999999999998)},
		{[]value.Value{value.Int(math.MaxInt64), value.Int(1), value.Int(-2)}, value.Int(math.MaxInt64 - 1), value.Double(float64(math.MaxInt64-1) / 3)},
	} {
		for _, perm := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			a = NewAggregator([]Aggregation{{Op: index.AggSum, Field: n, Alias: "s"}, {Op: index.AggAvg, Field: n, Alias: "a"}})
			for _, i := range perm {
				a.Add(&Doc{Name: root + "/c/x", Fields: value.MapOf(map[string]value.Value{"n": set.vals[i]})})
			}
			if r := a.Result(); !value.Equal(r["s"], set.sum) || !value.Equal(r["a"], set.avg) {
				t.Errorf("sum and avg of %v in order %v: %v, want %v and %v", set.vals, perm, r, set.sum, set.avg)
			}
		}
	}
	a = NewAggregator([]Aggregation{{Op: index.AggCount, UpTo: 3, Bounded: true, Alias: "c"}})
	added := 0
	for _, d := range candidates(t) {
		added++
		if a.Add(d) {
			break
		}
	}
	if added != 3 || !value.Equal(a.Result()["c"], value.Int(3)) {
		t.Errorf("count up_to 3: added %d, %v", added, a.Result())
	}
}
