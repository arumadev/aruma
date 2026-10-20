package core

import (
	"encoding/json"
	"errors"
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
