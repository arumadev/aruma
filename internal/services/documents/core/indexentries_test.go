package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// emptyCatalog is a database with no override and no composite.
func emptyCatalog() *catalog { return &catalog{found: true, fields: fieldConfigs{}} }

// keysOfIndex returns a document's entry keys of one index, in key order.
func keysOfIndex(d *docEntries, id int64) []string {
	var out []string
	for e := range d.set {
		if e.index == id {
			out = append(out, e.key)
		}
	}
	slices.Sort(out)
	return out
}

// The key layout, byte for byte (indexes.go): version, the collection with its 0x00, the value
// (inverted for descending), the name (inverted with __name__ descending) and its length.
func TestIndexEntryGoldenKeys(t *testing.T) {
	p := ParsePath("c/d")
	d := entriesOf(emptyCatalog(), fieldConfigs{}, p, fieldsOf("a", value.Int(1), "t", value.Array(value.Bool(true), value.Bool(true))), false)
	asc := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "a", index.Ascending))
	desc := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "a", index.Descending))
	contains := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "t", index.Contains))
	// 01 | 02 'c' 00 01 00 | 30 05 8000 0000000000000000 00 | 02 'c' 00 01 02 'd' 00 01 00 | 0009
	const coll, name = "0263000100", "026300010264000100" + "0009"
	want := map[string][]string{
		"asc":      {"01" + coll + "3005" + "8000" + "0000000000000000" + "00" + name},
		"desc":     {"01" + coll + "cffa" + "7fff" + "ffffffffffffffff" + "ff" + "fd9cfffefd9bfffeff" + "0009"},
		"contains": {"01" + coll + "2001" + name},
	}
	for label, got := range map[string][]string{"asc": asc, "desc": desc, "contains": contains} {
		var hexes []string
		for _, k := range got {
			hexes = append(hexes, hex.EncodeToString([]byte(k)))
		}
		if !slices.Equal(hexes, want[label]) {
			t.Errorf("%s: got %v, want %v", label, hexes, want[label])
		}
	}
	// The array's own ascending and descending entries, and nothing for t's array-contains of
	// a scalar.
	if n := len(d.set); n != 2+2+1 {
		t.Errorf("%d entries, want 5", n)
	}
	for _, k := range append(asc, desc...) {
		got, err := pathOfEntry([]byte(k))
		if err != nil || got.String() != "c/d" {
			t.Errorf("pathOfEntry(%x) = %v, %v", k, got, err)
		}
	}
}

// Which entries each value type gets from the automatic indexes: two for any value (ascending,
// descending), one more per distinct element of an array, and a map's subfields recursively;
// nothing inside an array, a vector or a BSON shape.
func TestIndexEntriesPerType(t *testing.T) {
	cases := []struct {
		name string
		v    value.Value
		want int
	}{
		{"null", value.Null(), 2},
		{"bool", value.Bool(false), 2},
		{"int", value.Int(-7), 2},
		{"double nan", value.Double(math.NaN()), 2},
		{"timestamp", value.Timestamp(1, 2), 2},
		{"string", value.String("x"), 2},
		{"bytes", value.Bytes([]byte{0, 0xff}), 2},
		{"reference", value.Reference("projects/p/databases/(default)/documents/c/d"), 2},
		{"geopoint", value.GeoPoint(1, 2), 2},
		{"vector", value.Vector(1, 2, 3), 2},
		{"bson int32", value.Int32(5), 2},
		{"empty array", value.Array(), 2},
		{"array", value.Array(value.Int(1), value.Double(1), value.String("a"), value.Map(value.Field{Name: "x", Value: value.Int(1)})), 2 + 3},
		{"empty map", value.Map(), 2},
		{"map", value.Map(value.Field{Name: "x", Value: value.Int(1)}, value.Field{Name: "y", Value: value.Array(value.Int(2))}), 2 + 2 + 3},
		{"nested", value.Map(value.Field{Name: "m", Value: value.Map(value.Field{Name: "z", Value: value.Null()})}), 2 + 2 + 2},
	}
	for _, c := range cases {
		d := entriesOf(emptyCatalog(), fieldConfigs{}, ParsePath("c/d"), fieldsOf("f", c.v), false)
		if len(d.set) != c.want {
			t.Errorf("%s: %d entries, want %d", c.name, len(d.set), c.want)
		}
	}
	// A subfield's index is its own path's.
	d := entriesOf(emptyCatalog(), fieldConfigs{}, ParsePath("c/d"), fieldsOf("m", value.Map(value.Field{Name: "odd key", Value: value.Int(1)})), false)
	if len(keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "m.`odd key`", index.Ascending))) != 1 {
		t.Error("no entry for m.`odd key`")
	}
}

// Within one index, entries order as their values do (value.Compare), descending ones in
// reverse, and ties by name: for random pairs of values and documents.
func TestIndexEntryOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	vals := []value.Value{value.Null(), value.MinKey(), value.Bool(false), value.Bool(true), value.Double(math.NaN()),
		value.Double(math.Inf(-1)), value.Int(-3), value.Double(-0.5), value.Int(0), value.Double(0.5), value.Int(7),
		value.Timestamp(-5, 0), value.Timestamp(5, 1), value.String(""), value.String("a"), value.String("a\x00"),
		value.String("ab"), value.String(strings.Repeat("x", 1600)), value.String(strings.Repeat("x", 1600) + "y"),
		value.Bytes(nil), value.Bytes([]byte{0xff}), value.Reference("projects/p/databases/(default)/documents/c/a"),
		value.GeoPoint(0, 0), value.Array(), value.Array(value.Int(1)), value.Array(value.Int(1), value.Null()),
		value.Vector(1), value.Map(), value.Map(value.Field{Name: "a", Value: value.Int(1)}), value.MaxKey()}
	names := []string{"c/a", "c/b", "c/__id5__", "c/a/s/x", "c/\x00"}
	key := func(v value.Value, name string, mode index.FieldMode) string {
		d := entriesOf(emptyCatalog(), fieldConfigs{}, ParsePath(name), fieldsOf("f", v), false)
		ks := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "f", mode))
		if len(ks) != 1 {
			t.Fatalf("%v: %d keys", v, len(ks))
		}
		return ks[0]
	}
	for range 3000 {
		a, b := vals[r.IntN(len(vals))], vals[r.IntN(len(vals))]
		na, nb := names[r.IntN(len(names))], names[r.IntN(len(names))]
		if ParsePath(na).CollectionID() != "c" || ParsePath(nb).CollectionID() != "c" {
			continue
		}
		want := value.Compare(a, b)
		if want == 0 {
			want = bytes.Compare(nameKey(ParsePath(na)), nameKey(ParsePath(nb)))
		}
		got := strings.Compare(key(a, na, index.Ascending), key(b, nb, index.Ascending))
		if sign(got) != sign(want) {
			t.Fatalf("ascending %v %s vs %v %s: %d, want %d", a, na, b, nb, got, want)
		}
		wantDesc := -value.Compare(a, b)
		if wantDesc == 0 {
			wantDesc = -bytes.Compare(nameKey(ParsePath(na)), nameKey(ParsePath(nb)))
		}
		if got := strings.Compare(key(a, na, index.Descending), key(b, nb, index.Descending)); sign(got) != sign(wantDesc) {
			t.Fatalf("descending %v %s vs %v %s: %d, want %d", a, na, b, nb, got, wantDesc)
		}
	}
}

// A collection's entries are one key range, apart from those of a deeper collection of the same
// id: c/x/c/y's keys sort after every key of the top-level c.
func TestIndexKeyCollectionRange(t *testing.T) {
	top := IndexKeyPrefix(index.ScopeCollection, nil, "c")
	end := IndexPrefixEnd(top)
	in := func(name string, v value.Value) bool {
		d := entriesOf(emptyCatalog(), fieldConfigs{}, ParsePath(name), fieldsOf("f", v), false)
		k := keysOfIndex(d, SingleFieldIndexID(ParsePath(name).CollectionID(), index.ScopeCollection, "f", index.Ascending))[0]
		return k >= string(top) && k < string(end)
	}
	if !in("c/a", value.MaxKey()) || !in("c/zzz", value.Null()) || in("c/a/c/b", value.Null()) || in("d/a", value.Null()) {
		t.Error("a collection's entries are not one range")
	}
	// A bound on value and name.
	lo := AppendIndexName(AppendIndexValue(bytes.Clone(top), value.Int(5), false), ParsePath("c/m"), false)
	d := entriesOf(emptyCatalog(), fieldConfigs{}, ParsePath("c/m"), fieldsOf("f", value.Int(5)), false)
	if k := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollection, "f", index.Ascending))[0]; k != string(lo) {
		t.Errorf("the built bound %x is not the entry %x", lo, k)
	}
}

// Overrides: an exemption removes a field's (and its subfields') automatic indexes, a
// collection-group override adds a COLLECTION_GROUP index with no collection in its keys, and
// '*' and __default__ govern every field without its own.
func TestIndexEntriesOverrides(t *testing.T) {
	sub := value.Map(value.Field{Name: "x", Value: value.Int(1)})
	doc := fieldsOf("a", value.Int(1), "m", sub)
	count := func(c fieldConfigs) int {
		return len(entriesOf(emptyCatalog(), c, ParsePath("c/d"), doc, false).set)
	}
	if n := count(fieldConfigs{}); n != 6 {
		t.Fatalf("automatic: %d entries, want 6", n)
	}
	if n := count(fieldConfigs{{"c", "m"}: {}}); n != 2 {
		t.Errorf("m exempted: %d entries, want 2 (a's)", n)
	}
	if n := count(fieldConfigs{{"c", "m"}: {}, {"c", "m.x"}: automaticIndexes}); n != 4 {
		t.Errorf("m exempted, m.x not: %d entries, want 4", n)
	}
	if n := count(fieldConfigs{{"c", index.AllFields}: {}}); n != 0 {
		t.Errorf("c/* exempted: %d entries", n)
	}
	if n := count(fieldConfigs{{"other", index.AllFields}: {}}); n != 6 {
		t.Errorf("another group's exemption: %d entries", n)
	}
	if n := count(fieldConfigs{{index.DefaultCollectionGroup, index.AllFields}: {{Scope: index.ScopeCollection, Mode: index.Ascending}}}); n != 3 {
		t.Errorf("default ascending only: %d entries, want 3", n)
	}
	cg := fieldConfigs{{"c", "a"}: {{Scope: index.ScopeCollectionGroup, Mode: index.Ascending}}}
	d := entriesOf(emptyCatalog(), cg, ParsePath("p/q/c/d"), doc, false)
	ks := keysOfIndex(d, SingleFieldIndexID("c", index.ScopeCollectionGroup, "a", index.Ascending))
	if len(ks) != 1 || !strings.HasPrefix(ks[0], string([]byte{value.KeyVersion, 0x30})) {
		t.Errorf("collection-group entry %x: want the value right after the version", ks)
	}
	if len(d.set) != 1+4 {
		t.Errorf("a's collection group index only: %d entries, want 5", len(d.set))
	}
}

// Composite entries: one per element of the array-contains field, none when a field is missing;
// a map path reaches into maps.
func TestCompositeEntries(t *testing.T) {
	ix := &composite{id: 9, group: "c", scope: index.ScopeCollection, fields: []compositeField{
		{path: value.FieldPath{"t"}, mode: index.Contains},
		{path: value.FieldPath{"m", "x"}, mode: index.Descending},
		{path: value.FieldPath{"__name__"}, mode: index.Descending},
	}}
	cat := &catalog{found: true, fields: fieldConfigs{}, composites: []*composite{ix}}
	full := fieldsOf("t", value.Array(value.Int(1), value.Int(2), value.Double(1)), "m", value.Map(value.Field{Name: "x", Value: value.String("v")}))
	if n := len(keysOfIndex(entriesOf(cat, cat.fields, ParsePath("c/d"), full, true), 9)); n != 2 {
		t.Errorf("%d entries, want 2", n)
	}
	for _, f := range []value.Value{fieldsOf("t", value.Array(value.Int(1))), fieldsOf("t", value.Array(), "m", value.Map(value.Field{Name: "x", Value: value.Int(1)})),
		fieldsOf("t", value.Int(1), "m", value.Map(value.Field{Name: "x", Value: value.Int(1)}))} {
		if n := len(keysOfIndex(entriesOf(cat, cat.fields, ParsePath("c/d"), f, true), 9)); n != 0 {
			t.Errorf("%v: %d entries, want none", f, n)
		}
	}
	if n := len(keysOfIndex(entriesOf(cat, cat.fields, ParsePath("e/d"), full, true), 9)); n != 0 {
		t.Errorf("another group: %d entries", n)
	}
	// __name__ descending: the name part inverted, and read back.
	k := keysOfIndex(entriesOf(cat, cat.fields, ParsePath("c/d"), full, true), 9)[0]
	if p, err := pathOfEntry([]byte(k)); err != nil || p.String() != "c/d" {
		t.Errorf("pathOfEntry: %v, %v", p, err)
	}
}

// recordedName is the recorder's document of f3-index-entries.json named with l bytes below
// documents/: aruma-ref-f3-ix/<id1>/c/<id2>/c/<id3>/c/<id4>/c/<id5>, the ids sharing the
// length, the first starting with the tag.
func recordedName(l int, tag string) Path {
	const ix = "aruma-ref-f3-ix"
	sum := l - (len(ix) + 5 + 8)
	ids := make([]string, 5)
	for i := range ids {
		n := sum / 5
		if i < sum%5 {
			n++
		}
		ids[i] = strings.Repeat("n", n)
		if i == 0 {
			ids[i] = tag + "-" + strings.Repeat("n", n-len(tag)-1)
		}
	}
	return ParsePath(ix + "/" + strings.Join(ids, "/c/"))
}

// A document's entries are held to production's limits as f3-index-entries.json recorded them:
// the entry size against the name's length (no field, one integer, a 1,500- or 3,000-byte
// string, a 1,500-byte field name), 40,000 entries with an array element counting two, and a
// name of 6,144 bytes at any depth (f0-errors.json).
func TestIndexEntryLimits(t *testing.T) {
	check := func(p Path, fields value.Value) error {
		return entriesOf(emptyCatalog(), fieldConfigs{}, p, fields, true).check()
	}
	for _, c := range []struct {
		kind   string
		fields value.Value
		max    int // the longest name accepted; one byte more is refused
	}{
		{"entry_no_fields", value.Map(), 4496},
		{"entry_int", fieldsOf("f", value.Int(1)), 2877},
		{"entry_string_1500", fieldsOf("f", value.String(strings.Repeat("s", 1500))), 2341},
		{"entry_string_3000", fieldsOf("f", value.String(strings.Repeat("s", 3000))), 2341},
		{"entry_long_field_name", fieldsOf(strings.Repeat("x", 1500), value.Int(1)), 2341},
	} {
		if p := recordedName(c.max, c.kind); len(p.String()) != c.max {
			t.Fatalf("%s: a name of %d bytes", c.kind, len(p.String()))
		}
		if err := check(recordedName(c.max, c.kind), c.fields); err != nil {
			t.Errorf("%s at %d: %v", c.kind, c.max, err)
		}
		if err := check(recordedName(c.max+1, c.kind), c.fields); err != ErrIndexEntryTooLarge {
			t.Errorf("%s at %d: %v", c.kind, c.max+1, err)
		}
	}
	long := ParsePath("c/" + strings.Repeat("n", 6144-16-2-1))
	if value.NameSize(long.String()) != 6144 {
		t.Fatalf("name size %d", value.NameSize(long.String()))
	}
	if err := check(long, value.Map()); err != ErrIndexEntryTooLarge {
		t.Errorf("6144-byte name: %v", err)
	}

	ints := func(n int) []value.Value {
		out := make([]value.Value, n)
		for i := range out {
			out[i] = value.Int(int64(i))
		}
		return out
	}
	scalars := func(n int) value.Value {
		fs := make([]value.Field, n)
		for i := range fs {
			fs[i] = value.Field{Name: fmt.Sprintf("f%05d", i), Value: value.Int(int64(i))}
		}
		return value.Map(fs...)
	}
	for _, c := range []struct {
		kind string
		of   func(n int) value.Value
		max  int
	}{
		{"fields", scalars, 20_000},
		{"array", func(n int) value.Value { return fieldsOf("a", value.Array(ints(n)...)) }, 19_999},
		{"map", func(n int) value.Value { return fieldsOf("m", scalars(n)) }, 19_999},
	} {
		p := ParsePath(fmt.Sprintf("aruma-ref-f3-ix/%s-%d", c.kind, c.max))
		if err := check(p, c.of(c.max)); err != nil {
			t.Errorf("%s %d: %v", c.kind, c.max, err)
		}
		p = ParsePath(fmt.Sprintf("aruma-ref-f3-ix/%s-%d", c.kind, c.max+1))
		err := check(p, c.of(c.max+1))
		if te, ok := errors.AsType[*TooManyIndexEntriesError](err); !ok || te.DocPath() != "/"+p.String() {
			t.Errorf("%s %d: %v", c.kind, c.max+1, err)
		}
	}
}

func TestIndexIDs(t *testing.T) {
	for _, id := range []int64{1, 127, 128, 1 << 40, 1<<62 - 1} {
		s := FormatIndexID(id)
		if got, ok := ParseIndexID(s); !ok || got != id {
			t.Errorf("%d → %q → %d %v", id, s, got, ok)
		}
	}
	if FormatIndexID(2777) != "CNkV" {
		t.Errorf("FormatIndexID(2777) = %q", FormatIndexID(2777))
	}
	for _, s := range []string{"", "x", "CA", "CNkV=", "CICAgOjXh4EKAA"} {
		if _, ok := ParseIndexID(s); ok {
			t.Errorf("ParseIndexID(%q) ok", s)
		}
	}
	a := SingleFieldIndexID("c", index.ScopeCollection, "a", index.Ascending)
	if a >= 0 || a == SingleFieldIndexID("c", index.ScopeCollection, "a", index.Descending) ||
		a == SingleFieldIndexID("c", index.ScopeCollectionGroup, "a", index.Ascending) || a == SingleFieldIndexID("d", index.ScopeCollection, "a", index.Ascending) {
		t.Error("single-field ids are not distinct and negative")
	}
}
