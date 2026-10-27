package index_test

import (
	"slices"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

func field(path string, mode index.FieldMode) index.IndexField {
	return index.IndexField{Path: path, Mode: mode}
}

func composite(group string, scope index.Scope, state index.State, fields ...index.IndexField) index.Index {
	return index.Index{CollectionGroup: group, Scope: scope, State: state, Fields: fields}
}

func filter(f index.Filter) *index.Filter { return &f }

// TestCheckCatalog covers what the corpus cannot: declared composites and field overrides,
// where the verdict is verified (exact indexes, collection-group enablement) and where it is
// left Uncertain (R18 to R20, unprobed shapes).
func TestCheckCatalog(t *testing.T) {
	const C, CG = index.ScopeCollection, index.ScopeCollectionGroup
	asc, desc, contains := index.Ascending, index.Descending, index.Contains
	eqA, eqB := index.Where("a", index.OpEqual), index.Where("b", index.OpEqual)
	coll := func(where *index.Filter, orders ...index.Order) index.QueryShape {
		return index.QueryShape{CollectionID: "c", Scope: C, Where: where, OrderBy: orders}
	}
	group := func(where *index.Filter, orders ...index.Order) index.QueryShape {
		return index.QueryShape{CollectionID: "c", Scope: CG, Where: where, OrderBy: orders}
	}
	byD := index.Order{Field: "d"}
	automatic := []index.SingleFieldIndex{{Scope: C, Mode: asc}, {Scope: C, Mode: desc}, {Scope: C, Mode: contains}}

	cases := []struct {
		name      string
		q         index.QueryShape
		indexes   []index.Index
		overrides []index.FieldOverride
		want      index.Verdict
		reason    index.Reason
	}{
		{
			name:    "exact composite, __name__ appended",
			q:       coll(filter(eqA), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.Served,
		},
		{
			name:    "equality prefix in another order and direction",
			q:       coll(filter(index.And(eqA, eqB)), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("b", desc), field("a", asc), field("d", asc))},
			want:    index.Served,
		},
		{
			name:    "composite on another collection group",
			q:       coll(filter(eqA), byD),
			indexes: []index.Index{composite("other", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:    "order direction differs",
			q:       coll(filter(eqA), index.Order{Field: "d", Desc: true}),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:    "__name__ direction differs (R5)",
			q:       coll(filter(eqA), byD, index.Order{Field: index.NameField, Desc: true}),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:    "an index with an extra field never serves",
			q:       coll(filter(eqA), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("b", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:    "collection-scope composite for a collection-group query",
			q:       group(filter(eqA), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name: "R18: merge of declared composites sharing the order",
			q:    coll(filter(index.And(eqA, eqB)), byD),
			indexes: []index.Index{
				composite("c", C, index.StateReady, field("a", asc), field("d", asc)),
				composite("c", C, index.StateReady, field("b", asc), field("d", asc)),
			},
			want: index.Uncertain, reason: index.ReasonMerge,
		},
		{
			name:    "R18 needs every equality covered",
			q:       coll(filter(index.And(eqA, eqB)), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:    "R19: collection-group composite for a collection query",
			q:       coll(filter(eqA), byD),
			indexes: []index.Index{composite("c", CG, index.StateReady, field("a", asc), field("d", asc))},
			want:    index.Uncertain, reason: index.ReasonSelection,
		},
		{
			name:    "R19: equality-pinned order field in the prefix",
			q:       coll(filter(index.And(eqA, index.Where("d", index.OpEqual))), byD),
			indexes: []index.Index{composite("c", C, index.StateReady, field("d", asc), field("a", asc))},
			want:    index.Uncertain, reason: index.ReasonSelection,
		},
		{
			name: "R19: aggregation fields in another order",
			q: index.QueryShape{CollectionID: "c", Scope: C, Aggregations: []index.Aggregation{
				{Op: index.AggSum, Field: "d"}, {Op: index.AggSum, Field: "b"},
			}},
			indexes: []index.Index{composite("c", C, index.StateReady, field("d", asc), field("b", asc))},
			want:    index.Uncertain, reason: index.ReasonSelection,
		},
		{
			name: "aggregation fields in the suggested order",
			q: index.QueryShape{CollectionID: "c", Scope: C, Aggregations: []index.Aggregation{
				{Op: index.AggSum, Field: "d"}, {Op: index.AggSum, Field: "b"},
			}},
			indexes: []index.Index{composite("c", C, index.StateReady, field("b", asc), field("d", asc))},
			want:    index.Served,
		},
		{
			name:    "R21: exact composite still CREATING",
			q:       coll(filter(eqA), byD),
			indexes: []index.Index{composite("c", C, index.StateCreating, field("a", asc), field("d", asc))},
			want:    index.NeedsIndex,
		},
		{
			name:      "R12: collection-group single-field index enabled by an override",
			q:         group(filter(eqA)),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: append(automatic, index.SingleFieldIndex{Scope: CG, Mode: asc})}},
			want:      index.Served,
		},
		{
			name:      "R12: the ascending override does not serve descending",
			q:         group(nil, index.Order{Field: "a", Desc: true}),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: append(automatic, index.SingleFieldIndex{Scope: CG, Mode: asc})}},
			want:      index.NeedsIndex,
		},
		{
			name:      "R12: collection-level override enables every field",
			q:         group(filter(index.And(eqA, eqB))),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: index.AllFields, Indexes: append(automatic, index.SingleFieldIndex{Scope: CG, Mode: asc})}},
			want:      index.Served,
		},
		{
			name:      "R12: collection-group override still building",
			q:         group(filter(eqA)),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: append(automatic, index.SingleFieldIndex{Scope: CG, Mode: asc, State: index.StateCreating})}},
			want:      index.NeedsIndex,
		},
		{
			name:      "R20: field exempted",
			q:         coll(filter(eqA)),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: []index.SingleFieldIndex{}}},
			want:      index.Uncertain, reason: index.ReasonExemption,
		},
		{
			name:      "R20: map subfield inherits its parent's exemption",
			q:         coll(nil, index.Order{Field: "m.x", Desc: true}),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "m", Indexes: []index.SingleFieldIndex{{Scope: C, Mode: asc}}}},
			want:      index.Uncertain, reason: index.ReasonExemption,
		},
		{
			name:      "R20: the parent keeps the ascending index",
			q:         coll(filter(index.Where("m.x", index.OpGreaterThan))),
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "m", Indexes: []index.SingleFieldIndex{{Scope: C, Mode: asc}}}},
			want:      index.Served,
		},
		{
			name:      "R20: database default without array-contains",
			q:         coll(filter(index.Where("tags", index.OpArrayContains))),
			overrides: []index.FieldOverride{{CollectionGroup: index.DefaultCollectionGroup, Path: index.AllFields, Indexes: automatic[:2]}},
			want:      index.Uncertain, reason: index.ReasonExemption,
		},
		{
			name:      "an exempted field still serves through a composite",
			q:         coll(filter(eqA), byD),
			indexes:   []index.Index{composite("c", C, index.StateReady, field("a", asc), field("d", asc))},
			overrides: []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: []index.SingleFieldIndex{}}},
			want:      index.Served,
		},
		{
			name: "unprobed: array-contains before __name__ descending",
			q:    coll(filter(index.Where("tags", index.OpArrayContains)), index.Order{Field: index.NameField, Desc: true}),
			want: index.Uncertain, reason: index.ReasonUnprobed,
		},
		{
			name: "collection group: array-contains before __name__ descending needs a composite",
			q:    group(filter(index.Where("tags", index.OpArrayContains)), index.Order{Field: index.NameField, Desc: true}),
			want: index.NeedsIndex,
		},
		{
			name: "unprobed: __name__ ordered before an inequality field",
			q:    coll(filter(index.Where("d", index.OpGreaterThan)), index.Order{Field: index.NameField}),
			want: index.Uncertain, reason: index.ReasonUnprobed,
		},
		{
			name: "unprobed: kindless query on a field",
			q:    index.QueryShape{Scope: CG, Where: filter(eqA)},
			want: index.Uncertain, reason: index.ReasonUnprobed,
		},
		{
			name: "kindless query on __name__",
			q:    index.QueryShape{Scope: CG, Where: filter(index.Where(index.NameField, index.OpGreaterThanOrEqual))},
			want: index.Served,
		},
		{
			name: "unknown operator",
			q:    coll(filter(index.Where("a", index.OpUnspecified))),
			want: index.Uncertain, reason: index.ReasonUnprobed,
		},
		{
			name: "R11: one OR disjunct uncertain, another missing",
			q:    coll(filter(index.Or(index.Where("tags", index.OpArrayContains), index.Where(index.NameField, index.OpGreaterThan))), index.Order{Field: index.NameField, Desc: true}),
			want: index.NeedsIndex,
		},
		{
			name: "R11: one OR disjunct uncertain, the other served",
			q:    coll(filter(index.Or(index.Where("tags", index.OpArrayContains), eqA)), index.Order{Field: index.NameField, Desc: true}),
			want: index.Uncertain, reason: index.ReasonUnprobed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cat, err := index.NewCatalog(c.indexes, c.overrides)
			if err != nil {
				t.Fatal(err)
			}
			d := index.Check(c.q, cat, index.Enforce)
			if d.Verdict != c.want || d.Reason != c.reason {
				t.Errorf("got %v %q (%s), want %v %q; missing %+v", d.Verdict, d.Reason, d.Detail, c.want, c.reason, d.Missing)
			}
			if d.Reject() != (c.want == index.NeedsIndex) {
				t.Errorf("Reject() = %v", d.Reject())
			}
		})
	}
}

func TestModes(t *testing.T) {
	for s, want := range map[string]index.Mode{"enforce": index.Enforce, "warn": index.Warn, "off": index.Off} {
		m, err := index.ParseMode(s)
		if err != nil || m != want || m.String() != s {
			t.Errorf("ParseMode(%q) = %v, %v", s, m, err)
		}
	}
	if _, err := index.ParseMode("strict"); err == nil {
		t.Error("ParseMode accepted strict")
	}

	eq := index.Where("a", index.OpEqual)
	q := index.QueryShape{CollectionID: "c", Scope: index.ScopeCollection, Where: &eq, OrderBy: []index.Order{{Field: "d"}}}
	if d := index.Check(q, nil, index.Enforce); !d.Reject() || d.Log() {
		t.Errorf("enforce: reject %v, log %v", d.Reject(), d.Log())
	}
	d := index.Check(q, nil, index.Warn)
	if d.Verdict != index.NeedsIndex || d.Reject() || !d.Log() || index.Suggest("p", index.DefaultDatabase, d) == nil {
		t.Errorf("warn: %v, reject %v, log %v", d.Verdict, d.Reject(), d.Log())
	}
	if d := index.Check(q, nil, index.Off); d.Verdict != index.Served || d.Reject() || d.Log() {
		t.Errorf("off: %v, reject %v, log %v", d.Verdict, d.Reject(), d.Log())
	}
}

// TestSuggestTemplates covers the template 4 kinds and a field path longer than a one-byte
// protobuf length.
func TestSuggestTemplates(t *testing.T) {
	q := func(where *index.Filter, orders ...index.Order) index.QueryShape {
		return index.QueryShape{CollectionID: "g", Scope: index.ScopeCollectionGroup, Where: where, OrderBy: orders}
	}
	cases := []struct {
		q    index.QueryShape
		want string
	}{
		{q(nil, index.Order{Field: "d", Desc: true}), "The query requires a COLLECTION_GROUP_DESC index for collection g and field d. You can create it here: https://console.firebase.google.com/v1/r/project/p/firestore/indexes?create_exemption=Cjpwcm9qZWN0cy9wL2RhdGFiYXNlcy8oZGVmYXVsdCkvY29sbGVjdGlvbkdyb3Vwcy9nL2ZpZWxkcy9kEAIaBQoBZBAC"},
		{q(filter(index.Where("t", index.OpArrayContainsAny))), "The query requires a COLLECTION_GROUP_CONTAINS index for collection g and field t. You can create it here: https://console.firebase.google.com/v1/r/project/p/firestore/indexes?create_exemption=Cjpwcm9qZWN0cy9wL2RhdGFiYXNlcy8oZGVmYXVsdCkvY29sbGVjdGlvbkdyb3Vwcy9nL2ZpZWxkcy90EAIaBQoBdBgB"},
		{q(filter(index.Where("a", index.OpEqual)), index.Order{Field: index.NameField, Desc: true}), "The query requires a COLLECTION_GROUP_DESC index for collection g and field a. You can create it here: https://console.firebase.google.com/v1/r/project/p/firestore/indexes?create_exemption=Cjpwcm9qZWN0cy9wL2RhdGFiYXNlcy8oZGVmYXVsdCkvY29sbGVjdGlvbkdyb3Vwcy9nL2ZpZWxkcy9hEAIaBQoBYRAC"},
	}
	for _, c := range cases {
		d := index.Check(c.q, nil, index.Enforce)
		if e := index.Suggest("p", index.DefaultDatabase, d); e == nil || e.Message != c.want {
			t.Errorf("%+v: got %v, want %q", c.q, e, c.want)
		}
	}

	long := string(make([]byte, 200))
	r := index.Required{Index: index.Index{CollectionGroup: "g", Scope: index.ScopeCollection, Fields: []index.IndexField{field(long, index.Ascending)}}}
	b := index.EncodeIndex("p", index.DefaultDatabase, r)
	// The field: tag 1a, length 205 (0a c8 01, the 200 bytes, 10 01) as the varint cd 01, then
	// the path's tag 0a and length 200 as c8 01.
	name := len("projects/p/databases/(default)/collectionGroups/g/indexes/_")
	if got := b[2+name+2 : 2+name+2+6]; string(got) != "\x1a\xcd\x01\x0a\xc8\x01" {
		t.Errorf("long field encodes as % x", got)
	}
}

func TestIndexSpec(t *testing.T) {
	ix := composite("c", index.ScopeCollection, index.StateReady, field("a", index.Ascending), field("d", index.Descending)).Normalize()
	if last := ix.Fields[len(ix.Fields)-1]; last != field(index.NameField, index.Descending) {
		t.Errorf("Normalize appended %+v", last)
	}
	ix = composite("c", index.ScopeCollection, index.StateReady, field("t", index.Contains)).Normalize()
	if last := ix.Fields[len(ix.Fields)-1]; last != field(index.NameField, index.Ascending) {
		t.Errorf("Normalize after array-contains appended %+v", last)
	}
	bad := []index.Index{
		composite("", index.ScopeCollection, index.StateReady, field("a", index.Ascending)),
		composite("c", 0, index.StateReady, field("a", index.Ascending)),
		composite("c", index.ScopeCollection, index.StateReady, field("a", index.Ascending), field("a", index.Descending)),
		composite("c", index.ScopeCollection, index.StateReady, field(index.NameField, index.Ascending), field("a", index.Ascending)),
		composite("c", index.ScopeCollection, index.StateReady, field("t", index.Contains), field("u", index.Contains)),
		composite("c", index.ScopeCollection, index.StateReady, field("a", 9)),
	}
	for _, ix := range bad {
		if _, err := index.NewCatalog([]index.Index{ix}, nil); err == nil {
			t.Errorf("NewCatalog accepted %+v", ix)
		}
	}
	badOverrides := [][]index.FieldOverride{
		{{CollectionGroup: index.DefaultCollectionGroup, Path: "a"}},
		{{CollectionGroup: "c", Path: "a"}, {CollectionGroup: "c", Path: "a"}},
		{{CollectionGroup: "c", Path: "a", Indexes: []index.SingleFieldIndex{{Scope: 3, Mode: index.Ascending}}}},
	}
	for _, o := range badOverrides {
		if _, err := index.NewCatalog(nil, o); err == nil {
			t.Errorf("NewCatalog accepted %+v", o)
		}
	}
}

// TestPermutedDisjunctsAskForOneIndex: (a == 1 && b == 1) || (b == 2 && a == 2) orderBy d needs
// one index, [a, b, d, __name__], which serves both disjuncts; a different equality set still
// asks for its own.
func TestPermutedDisjunctsAskForOneIndex(t *testing.T) {
	eq := func(f string) index.Filter { return index.Where(f, index.OpEqual) }
	q := func(w index.Filter) index.QueryShape {
		return index.QueryShape{CollectionID: "c", Scope: index.ScopeCollection, Where: &w, OrderBy: []index.Order{{Field: "d"}}}
	}
	d := index.Check(q(index.Or(index.And(eq("a"), eq("b")), index.And(eq("b"), eq("a")))), nil, index.Enforce)
	if d.Verdict != index.NeedsIndex || len(d.Missing) != 1 {
		t.Fatalf("permuted disjuncts: %v, %d missing: %+v", d.Verdict, len(d.Missing), d.Missing)
	}
	want := []index.IndexField{field("a", index.Ascending), field("b", index.Ascending), field("d", index.Ascending), field(index.NameField, index.Ascending)}
	if got := d.Missing[0].Index.Fields; !slices.Equal(got, want) {
		t.Errorf("asks for %v, want %v", got, want)
	}
	cat, err := index.NewCatalog([]index.Index{d.Missing[0].Index}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := index.Check(q(index.Or(index.And(eq("a"), eq("b")), index.And(eq("b"), eq("a")))), cat, index.Enforce).Verdict; v != index.Served {
		t.Errorf("with that index declared: %v", v)
	}
	d = index.Check(q(index.Or(index.And(eq("a"), eq("b")), index.And(eq("b"), eq("e")))), nil, index.Enforce)
	if len(d.Missing) != 2 {
		t.Errorf("different equality sets: %d missing: %+v", len(d.Missing), d.Missing)
	}
}

// TestLargeAndIsLinear: the DNF of an AND of many leaves is built in linear time. The query
// package refuses such filters long before; this keeps a malformed shape cheap.
func TestLargeAndIsLinear(t *testing.T) {
	leaves := make([]index.Filter, 100_000)
	for i := range leaves {
		leaves[i] = index.Where("a", index.OpEqual)
	}
	w := index.And(leaves...)
	start := time.Now()
	index.Check(index.QueryShape{CollectionID: "c", Scope: index.ScopeCollection, Where: &w}, nil, index.Enforce)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("an AND of %d leaves took %v", len(leaves), d)
	}
}
