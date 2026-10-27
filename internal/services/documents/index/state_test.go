package index_test

import (
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// TestCreatingIndexNotUsed: the planner never serves from an index that is still CREATING (R21,
// parity exception index-still-building). A query that needs one gets the needs-index error as if
// the index were absent, and is served once the same index is READY.
func TestCreatingIndexNotUsed(t *testing.T) {
	const C = index.ScopeCollection
	q := index.QueryShape{
		CollectionID: "c", Scope: C,
		Where:   &index.Filter{Field: "a", Op: index.OpEqual},
		OrderBy: []index.Order{{Field: "d"}},
	}
	fields := []index.IndexField{{Path: "a", Mode: index.Ascending}, {Path: "d", Mode: index.Ascending}}
	check := func(state index.State) index.Decision {
		t.Helper()
		cat, err := index.NewCatalog([]index.Index{{CollectionGroup: "c", Scope: C, State: state, Fields: fields}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return index.Check(q, cat, index.Enforce)
	}

	d := check(index.StateCreating)
	if d.Verdict != index.NeedsIndex || !d.Reject() {
		t.Fatalf("CREATING composite: verdict %v, reject %v, want needs_index and rejected", d.Verdict, d.Reject())
	}
	if len(d.Missing) != 1 || d.Missing[0].Exemption {
		t.Fatalf("Missing = %+v, want the one composite index (template 1)", d.Missing)
	}
	if e := index.Suggest("p", index.DefaultDatabase, d); e == nil || len(e.Links) != 1 || e.Links[0].Param != index.ParamComposite {
		t.Fatalf("Suggest = %+v, want the composite-index error with one console link", e)
	}
	if d := check(index.StateReady); d.Verdict != index.Served {
		t.Fatalf("READY composite: verdict %v, want served", d.Verdict)
	}

	// A single-field index still building is not served from either.
	cg := index.QueryShape{CollectionID: "c", Scope: index.ScopeCollectionGroup, Where: &index.Filter{Field: "a", Op: index.OpEqual}}
	cat, err := index.NewCatalog(nil, []index.FieldOverride{{CollectionGroup: "c", Path: "a", Indexes: []index.SingleFieldIndex{
		{Scope: C, Mode: index.Ascending}, {Scope: C, Mode: index.Descending}, {Scope: C, Mode: index.Contains},
		{Scope: index.ScopeCollectionGroup, Mode: index.Ascending, State: index.StateCreating},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if d := index.Check(cg, cat, index.Enforce); d.Verdict != index.NeedsIndex {
		t.Fatalf("CREATING single-field index: verdict %v, want needs_index", d.Verdict)
	}
}
