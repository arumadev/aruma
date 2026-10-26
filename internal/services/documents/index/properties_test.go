package index_test

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// genShape draws a query shape from a small universe of fields and operators, so that the
// properties below meet every rule: equalities, inequalities, array-contains, map subfields,
// __name__ filters and orders, OR, aggregations and collection groups.
func genShape(r *rand.Rand) index.QueryShape {
	values := []string{"a", "b", "d", "m.x"}
	ops := []index.Operator{
		index.OpEqual, index.OpEqual, index.OpIn, index.OpIsNull, index.OpLessThan,
		index.OpGreaterThan, index.OpNotEqual, index.OpNotIn, index.OpIsNotNull,
	}
	leaf := func() index.Filter {
		switch r.IntN(8) {
		case 0:
			return index.Where("tags", []index.Operator{index.OpArrayContains, index.OpArrayContainsAny}[r.IntN(2)])
		case 1:
			return index.Where(index.NameField, []index.Operator{index.OpGreaterThan, index.OpEqual}[r.IntN(2)])
		}
		return index.Where(values[r.IntN(len(values))], ops[r.IntN(len(ops))])
	}
	leaves := func(n int) []index.Filter {
		fs := make([]index.Filter, n)
		for i := range fs {
			fs[i] = leaf()
		}
		return fs
	}
	q := index.QueryShape{CollectionID: "c", Scope: index.ScopeCollection}
	if r.IntN(4) == 0 {
		q.CollectionID, q.Scope = "cg", index.ScopeCollectionGroup
	}
	var f index.Filter
	switch r.IntN(7) {
	case 0:
	case 1, 2:
		f = leaf()
	case 3, 4:
		f = index.And(leaves(2 + r.IntN(2))...)
	case 5:
		f = index.Or(leaves(2)...)
	case 6:
		f = index.And(leaf(), index.Or(leaves(2)...))
	}
	if f.Op != index.OpUnspecified {
		q.Where = &f
	}
	orderable := []string{"a", "b", "d"}
	r.Shuffle(len(orderable), func(i, j int) { orderable[i], orderable[j] = orderable[j], orderable[i] })
	for _, o := range orderable[:r.IntN(3)] {
		q.OrderBy = append(q.OrderBy, index.Order{Field: o, Desc: r.IntN(2) == 0})
	}
	if r.IntN(4) == 0 {
		q.OrderBy = append(q.OrderBy, index.Order{Field: index.NameField, Desc: r.IntN(2) == 0})
	}
	switch r.IntN(6) {
	case 0:
		q.Aggregations = []index.Aggregation{{Op: index.AggCount}}
	case 1:
		q.Aggregations = []index.Aggregation{{Op: index.AggSum, Field: values[r.IntN(len(values))]}}
	case 2:
		q.Aggregations = []index.Aggregation{
			{Op: index.AggSum, Field: values[r.IntN(len(values))]},
			{Op: index.AggAvg, Field: values[r.IntN(len(values))]},
		}
	}
	return q
}

const propertyShapes = 20000

// catalogBuilder declares what a database has, the way the console link would add it.
type catalogBuilder struct {
	composites []index.Index
	overrides  map[[2]string][]index.SingleFieldIndex
}

// add declares a required index: a composite as suggested, or for an exemption the field's
// override with the automatic default kept and the collection-group index added, as the
// console's exemption form does.
func (b *catalogBuilder) add(r index.Required) {
	if !r.Exemption {
		b.composites = append(b.composites, r.Index)
		return
	}
	if b.overrides == nil {
		b.overrides = map[[2]string][]index.SingleFieldIndex{}
	}
	k := [2]string{r.Index.CollectionGroup, r.Index.Fields[0].Path}
	if _, ok := b.overrides[k]; !ok {
		b.overrides[k] = []index.SingleFieldIndex{
			{Scope: index.ScopeCollection, Mode: index.Ascending},
			{Scope: index.ScopeCollection, Mode: index.Descending},
			{Scope: index.ScopeCollection, Mode: index.Contains},
		}
	}
	b.overrides[k] = append(b.overrides[k], index.SingleFieldIndex{Scope: r.Index.Scope, Mode: r.Index.Fields[0].Mode})
}

func (b *catalogBuilder) catalog(t *testing.T) *index.Catalog {
	t.Helper()
	var overrides []index.FieldOverride
	for k, ixs := range b.overrides {
		overrides = append(overrides, index.FieldOverride{CollectionGroup: k[0], Path: k[1], Indexes: ixs})
	}
	cat, err := index.NewCatalog(b.composites, overrides)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// disjunctions counts the OR nodes, so a shape without any has one DNF disjunct.
func disjunctions(f *index.Filter) int {
	if f == nil {
		return 0
	}
	n := 0
	if f.Op == index.OpOr {
		n++
	}
	for i := range f.Filters {
		n += disjunctions(&f.Filters[i])
	}
	return n
}

// TestSuggestedIndexServes: declaring exactly what the error suggests makes the query served.
// Every corpus rejection, and every generated one. A generated query with OR may stay Uncertain
// when another disjunct was already Uncertain; with one disjunct it must be Served.
func TestSuggestedIndexServes(t *testing.T) {
	c := loadCorpus(t)
	for _, cs := range c.Cases {
		if cs.Verdict == "ok" {
			continue
		}
		q, err := decodeShape(cs.Request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var b catalogBuilder
		for _, l := range cs.Links {
			b.add(decodeIndex(t, l.Index))
		}
		if d := index.Check(q, b.catalog(t), index.Enforce); d.Verdict != index.Served {
			t.Errorf("%s with its suggested indexes: %v %s %s", cs.ID, d.Verdict, d.Reason, d.Detail)
		}
	}

	r := rand.New(rand.NewPCG(1, 2))
	rejected := 0
	for range propertyShapes {
		q := genShape(r)
		d := index.Check(q, nil, index.Enforce)
		if d.Verdict != index.NeedsIndex {
			continue
		}
		rejected++
		var b catalogBuilder
		for _, m := range d.Missing {
			b.add(m)
		}
		after := index.Check(q, b.catalog(t), index.Enforce)
		if after.Verdict == index.NeedsIndex || disjunctions(q.Where) == 0 && after.Verdict != index.Served {
			t.Fatalf("%+v with %+v: %v %s %s", q, d.Missing, after.Verdict, after.Reason, after.Detail)
		}
	}
	if rejected < propertyShapes/10 {
		t.Fatalf("only %d of %d generated shapes need an index", rejected, propertyShapes)
	}
}

// removeLeaf returns f without its i-th field filter (depth first), dropping composites left
// empty; nil when nothing is left.
func removeLeaf(f *index.Filter, i int) *index.Filter {
	n := 0
	var walk func(f index.Filter) (index.Filter, bool)
	walk = func(f index.Filter) (index.Filter, bool) {
		if f.Op != index.OpAnd && f.Op != index.OpOr {
			n++
			return f, n-1 != i
		}
		var kept []index.Filter
		for _, c := range f.Filters {
			if c, ok := walk(c); ok {
				kept = append(kept, c)
			}
		}
		f.Filters = kept
		return f, len(kept) > 0
	}
	out, ok := walk(*f)
	if !ok {
		return nil
	}
	return &out
}

func leafCount(f *index.Filter) int {
	if f == nil {
		return 0
	}
	if f.Op != index.OpAnd && f.Op != index.OpOr {
		return 1
	}
	n := 0
	for i := range f.Filters {
		n += leafCount(&f.Filters[i])
	}
	return n
}

// isKeyDescending is whether the only missing index is [__name__ DESC], the descending key scan.
func isKeyDescending(d index.Decision) bool {
	return len(d.Missing) == 1 && !d.Missing[0].Exemption &&
		slices.Equal(d.Missing[0].Index.Fields, []index.IndexField{{Path: index.NameField, Mode: index.Descending}})
}

// TestRemovingFilter: removing a filter from a query the automatic indexes serve never makes it
// need an index, with one exception the rules force. The property does not hold in general:
//
//   - With automatic indexes only, an equality-like filter is what lets __name__ descending be
//     served: a == 1 orderBy __name__ desc rides the descending index of a (R1,
//     eq1_ord_name_desc), but orderBy __name__ desc alone needs the composite [__name__ DESC],
//     because the key index only ascends (R5, name_desc). Every failure here is that one.
//   - With declared composites, an index serves the query it was made for and not its subsets:
//     [a, b, d] serves a == 1 && b == 1 orderBy d, not a == 1 orderBy d, which needs [a, d]. So
//     the generated shapes run with no catalogue; TestRemovingFilterCounterexamples pins both.
func TestRemovingFilter(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	served, checked, keyDesc := 0, 0, 0
	for range propertyShapes {
		q := genShape(r)
		if index.Check(q, nil, index.Enforce).Verdict != index.Served {
			continue
		}
		served++
		for i := range leafCount(q.Where) {
			smaller := q
			smaller.Where = removeLeaf(q.Where, i)
			checked++
			d := index.Check(smaller, nil, index.Enforce)
			switch {
			case d.Verdict != index.NeedsIndex:
			case isKeyDescending(d):
				keyDesc++
			default:
				t.Fatalf("%+v is served, but without filter %d it needs %+v", q, i, d.Missing)
			}
		}
	}
	if served < propertyShapes/10 || checked < propertyShapes/10 {
		t.Fatalf("only %d served shapes, %d removals", served, checked)
	}
	t.Logf("%d removals from %d served shapes; %d needed [__name__ DESC]", checked, served, keyDesc)
}

func TestRemovingFilterCounterexamples(t *testing.T) {
	c := index.QueryShape{CollectionID: "c", Scope: index.ScopeCollection}
	eq := index.Where("a", index.OpEqual)

	withEq := c
	withEq.Where, withEq.OrderBy = &eq, []index.Order{{Field: index.NameField, Desc: true}}
	without := withEq
	without.Where = nil
	if d := index.Check(withEq, nil, index.Enforce); d.Verdict != index.Served {
		t.Errorf("a == 1 orderBy __name__ desc: %v", d.Verdict)
	}
	if d := index.Check(without, nil, index.Enforce); !isKeyDescending(d) {
		t.Errorf("orderBy __name__ desc: %v %+v", d.Verdict, d.Missing)
	}

	cat, err := index.NewCatalog([]index.Index{{CollectionGroup: "c", Scope: index.ScopeCollection, Fields: []index.IndexField{
		{Path: "a", Mode: index.Ascending}, {Path: "b", Mode: index.Ascending}, {Path: "d", Mode: index.Ascending},
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	both := index.And(eq, index.Where("b", index.OpEqual))
	wide := c
	wide.Where, wide.OrderBy = &both, []index.Order{{Field: "d"}}
	narrow := wide
	narrow.Where = &eq
	if d := index.Check(wide, cat, index.Enforce); d.Verdict != index.Served {
		t.Errorf("a == 1 && b == 1 orderBy d with [a, b, d]: %v", d.Verdict)
	}
	if d := index.Check(narrow, cat, index.Enforce); d.Verdict != index.NeedsIndex {
		t.Errorf("a == 1 orderBy d with [a, b, d]: %v", d.Verdict)
	}
}

// permuteAnds shuffles the children of every AND node.
func permuteAnds(r *rand.Rand, f index.Filter) index.Filter {
	if f.Op != index.OpAnd && f.Op != index.OpOr {
		return f
	}
	kids := make([]index.Filter, len(f.Filters))
	for i, c := range f.Filters {
		kids[i] = permuteAnds(r, c)
	}
	if f.Op == index.OpAnd {
		r.Shuffle(len(kids), func(i, j int) { kids[i], kids[j] = kids[j], kids[i] })
	}
	f.Filters = kids
	return f
}

// TestPermutingAnds: the order of AND filters never changes the verdict, the message template
// or the number of missing indexes. (The order of equality fields inside a suggested index
// follows the query, R16, so the links may differ.) It runs with no catalogue and with one that
// holds what the unpermuted query was told to create.
func TestPermutingAnds(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range propertyShapes {
		q := genShape(r)
		if q.Where == nil {
			continue
		}
		d := index.Check(q, nil, index.Enforce)
		var b catalogBuilder
		for _, m := range d.Missing {
			b.add(m)
		}
		cat := b.catalog(t)
		withIndexes := index.Check(q, cat, index.Enforce)

		p := q
		f := permuteAnds(r, *q.Where)
		p.Where = &f
		pd := index.Check(p, nil, index.Enforce)
		if pd.Verdict != d.Verdict || pd.MultipleRangeFields != d.MultipleRangeFields || len(pd.Missing) != len(d.Missing) {
			t.Fatalf("%+v: %v %d, permuted %+v: %v %d", q, d.Verdict, len(d.Missing), p, pd.Verdict, len(pd.Missing))
		}
		if pw := index.Check(p, cat, index.Enforce); pw.Verdict != withIndexes.Verdict {
			t.Fatalf("%+v with %+v: %v, permuted %+v: %v", q, d.Missing, withIndexes.Verdict, p, pw.Verdict)
		}
	}
}
