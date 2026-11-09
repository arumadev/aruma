package core

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// spec reads a wire query object for tests.
func spec(t *testing.T, s string) QuerySpec {
	t.Helper()
	var w struct {
		SP  json.RawMessage `json:"sp"`
		SN  *string         `json:"sn"`
		SIN *bool           `json:"sin"`
		EP  json.RawMessage `json:"ep"`
		EN  *string         `json:"en"`
		EIN *bool           `json:"ein"`
		L   int64           `json:"l"`
		VF  string          `json:"vf"`
		I   string          `json:"i"`
	}
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		t.Fatal(err)
	}
	return QuerySpec{Index: w.I, StartValue: w.SP, StartName: w.SN, StartInclusive: w.SIN,
		EndValue: w.EP, EndName: w.EN, EndInclusive: w.EIN, Limit: w.L, FromRight: w.VF == "r"}
}

func TestQueryKeys(t *testing.T) {
	data := mustParse(t, `{
		"a": {"s": 3, "n": "x"},
		"b": {"s": 1},
		"c": {".priority": 2, "s": 2},
		"d": {"s": 0},
		"e": {".priority": 1, "s": "str"},
		"f": 5,
		"g": {"t": 1}
	}`)
	cases := []struct {
		q, want string
	}{
		// Priority order: no priority first (by key), then numbers, then strings.
		{`{}`, "a b d f g e c"},
		{`{"l":2,"vf":"l"}`, "a b"},
		{`{"l":2,"vf":"r"}`, "e c"},
		{`{"sp":1,"sin":true}`, "e c"},
		{`{"sp":null,"ep":null}`, "a b d f g"},
		// Key order and key ranges (startAt/endAt/startAfter/endBefore).
		{`{"i":".key","sp":"b","ep":"d"}`, "b c d"},
		{`{"i":".key","sp":"b","sin":false,"ep":"d","ein":false}`, "c"},
		{`{"i":".key","l":3,"vf":"r"}`, "e f g"},
		{`{"i":".key","sp":"c","ep":"c"}`, "c"}, // equalTo
		// Child order: missing values first as null, then numbers, strings, by key on ties.
		{`{"i":"s"}`, "f g d b c a e"},
		{`{"i":"s","l":2,"vf":"l"}`, "f g"},
		{`{"i":"s","sp":1,"ep":3}`, "b c a"},
		{`{"i":"s","sp":1,"sn":"b","sin":false}`, "c a e"},
		// startAfter(1) without a key sends [MAX_NAME]; endBefore(3) sends [MIN_NAME].
		{`{"i":"s","sp":1,"sn":"[MAX_NAME]","sin":false,"ep":3,"en":"[MIN_NAME]","ein":false}`, "c"},
		{`{"i":"s","sp":2,"ep":2}`, "c"}, // equalTo(2)
		{`{"i":"s","sp":"str","ep":"str"}`, "e"},
		{`{"i":"n","sp":"x","ep":"x"}`, "a"},
		// Value order: the leaf first, then the objects by key.
		{`{"i":".value","l":1,"vf":"l"}`, "f"},
		{`{"i":".value","sp":5,"ep":5}`, "f"},
	}
	for _, c := range cases {
		q, err := NewQuery(spec(t, c.q))
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		if got := strings.Join(q.Keys(data), " "); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
	// A leaf has no children to select; a query's result drops the node's own priority.
	q, _ := NewQuery(spec(t, `{"l":1,"vf":"l"}`))
	if q.Filter(Number(1)) != nil {
		t.Error("query on a leaf")
	}
	if got := q.Filter(mustParse(t, `{".priority":5,"a":1,"b":2}`)); string(got.ExportJSON()) != `{"a":1}` {
		t.Errorf("filtered: %s", got.ExportJSON())
	}
}

func TestQueryIdentity(t *testing.T) {
	a, _ := NewQuery(spec(t, `{"l":2,"vf":"r","i":"s"}`))
	b, _ := NewQuery(spec(t, `{"i":"s","vf":"r","l":2}`))
	c, _ := NewQuery(spec(t, `{"i":"s","vf":"l","l":2}`))
	if a.ID() != b.ID() || a.ID() == c.ID() {
		t.Errorf("ids: %s %s %s", a.ID(), b.ID(), c.ID())
	}
	d, _ := NewQuery(spec(t, `{}`))
	e, _ := NewQuery(spec(t, `{"i":".priority"}`))
	if !d.IsDefault() || !e.IsDefault() || d.ID() != DefaultQuery.ID() || e.ID() != DefaultQuery.ID() {
		t.Error("the default query has one identity")
	}
	k, _ := NewQuery(spec(t, `{"i":".key"}`))
	if k.IsDefault() || !k.LoadsAll() || k.IndexKey() != "" {
		t.Error("key order without bounds")
	}
	for bad, want := range map[string]error{
		`{"i":"a.b"}`: ErrInvalidPath, `{"i":"$bad"}`: ErrInvalidPath, // golden listen_errors.json
		`{"l":-1}`:            ErrInvalidLimit,
		`{"i":".key","sp":1}`: ErrInvalidQuery, `{"sp":{"a":1}}`: ErrInvalidQuery, `{"sp":{".sv":"timestamp"}}`: ErrInvalidQuery,
	} {
		if _, err := NewQuery(spec(t, bad)); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", bad, err, want)
		}
	}
}

// Keys with a limit puts only the selected children in order; it must select exactly what a
// full sort, the range, then the first or last Limit select.
func TestQueryKeysMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	values := []string{`null`, `false`, `true`, `0`, `1`, `2.5`, `-3`, `"a"`, `"b"`, `{"x": 1}`}
	keyBounds := []string{`"0"`, `"1"`, `"5"`, `"a"`, `"m"`}
	for round := range 300 {
		doc := map[string]json.RawMessage{}
		for i := range rng.IntN(60) {
			k := fmt.Sprint(i)
			if rng.IntN(3) == 0 {
				k = fmt.Sprintf("k%02d", i)
			}
			doc[k] = json.RawMessage(fmt.Sprintf(`{"s": %s, ".priority": %d}`, values[rng.IntN(len(values))], rng.IntN(4)))
		}
		b, _ := json.Marshal(doc)
		n := mustParse(t, string(b))
		for _, index := range []string{".key", ".priority", ".value", "s"} {
			obj := map[string]any{"i": index}
			if l := rng.IntN(12); l > 0 {
				obj["l"] = l
				if rng.IntN(2) == 0 {
					obj["vf"] = "r"
				}
			}
			bounds := values[1 : len(values)-1] // no null, no object: neither may bound a range
			if index == ".key" {
				bounds = keyBounds
			}
			if rng.IntN(3) == 0 {
				obj["sp"] = json.RawMessage(bounds[rng.IntN(len(bounds))])
			}
			if rng.IntN(3) == 0 {
				obj["ep"] = json.RawMessage(bounds[rng.IntN(len(bounds))])
			}
			qs, _ := json.Marshal(obj)
			q, err := NewQuery(spec(t, string(qs)))
			if err != nil {
				t.Fatalf("%s: %v", qs, err)
			}
			want := slices.DeleteFunc(n.SortedKeys(q.Index), func(k string) bool { return !q.inRange(k, n.Child(k)) })
			if q.Limit > 0 && len(want) > q.Limit {
				if q.FromRight {
					want = want[len(want)-q.Limit:]
				} else {
					want = want[:q.Limit]
				}
			}
			if got := q.Keys(n); !slices.Equal(got, want) {
				t.Fatalf("round %d, query %s on %s:\n got  %q\n want %q", round, qs, b, got, want)
			}
		}
	}
}

// A window kept by Update through random changes to a node's children is always Filter's on the
// node, and Update gives up only when the window loses a child the limit had kept.
func TestQueryUpdateMatchesFilter(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 2))
	values := []string{`null`, `false`, `true`, `0`, `1`, `2.5`, `-3`, `"a"`, `"b"`, `{"x": 1}`}
	keyBounds := []string{`"0"`, `"1"`, `"5"`, `"a"`, `"m"`}
	keys := []string{"0", "1", "2", "5", "10", "a", "b", "k01", "k02", "m", "z"}
	child := func() *Node {
		if rng.IntN(5) == 0 {
			return nil
		}
		return mustParse(t, fmt.Sprintf(`{"s": %s, ".priority": %d}`, values[rng.IntN(len(values))], rng.IntN(4)))
	}
	updated, filtered := 0, 0
	for round := range 200 {
		var n *Node
		for _, k := range keys {
			if rng.IntN(2) == 0 {
				n = n.Set(Path{k}, child())
			}
		}
		for _, index := range []string{".key", ".priority", ".value", "s"} {
			obj := map[string]any{"i": index}
			if l := rng.IntN(6); l > 0 {
				obj["l"] = l
				if rng.IntN(2) == 0 {
					obj["vf"] = "r"
				}
			}
			bounds := values[1 : len(values)-1]
			if index == ".key" {
				bounds = keyBounds
			}
			if rng.IntN(3) == 0 {
				obj["sp"] = json.RawMessage(bounds[rng.IntN(len(bounds))])
			}
			if rng.IntN(3) == 0 {
				obj["ep"] = json.RawMessage(bounds[rng.IntN(len(bounds))])
			}
			qs, _ := json.Marshal(obj)
			q, err := NewQuery(spec(t, string(qs)))
			if err != nil {
				t.Fatalf("%s: %v", qs, err)
			}
			cur, w := n, q.Filter(n)
			for range 30 {
				var touched []string
				for range 1 + rng.IntN(3) {
					k := keys[rng.IntN(len(keys))]
					cur = cur.Set(Path{k}, child())
					touched = append(touched, k)
				}
				next, ok := q.Update(w, cur, touched)
				if ok {
					updated++
				} else {
					filtered++
					next = q.Filter(cur)
				}
				if want := q.Filter(cur); !Equal(next, want) {
					t.Fatalf("round %d, query %s, after changes to %q (updated: %v):\n got  %s\n want %s",
						round, qs, touched, ok, next.ExportJSON(), want.ExportJSON())
				}
				w = next
			}
		}
	}
	if updated < 4*filtered {
		t.Errorf("Update gave up %d times and succeeded %d: it should rarely need Filter", filtered, updated)
	}
}

func TestSmallest(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 1))
	for range 500 {
		es := make([]int, rng.IntN(40))
		for i := range es {
			es[i] = rng.IntN(20)
		}
		k := rng.IntN(45) - 2
		want := slices.Sorted(slices.Values(es))
		want = want[:max(0, min(k, len(want)))]
		if got := smallest(slices.Clone(es), k, cmp.Compare[int]); !slices.Equal(got, want) {
			t.Fatalf("smallest(%v, %d) = %v, want %v", es, k, got, want)
		}
	}
}
