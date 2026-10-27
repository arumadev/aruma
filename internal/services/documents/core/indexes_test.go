package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

// smallBuilds makes builds take many batches.
var smallBuilds = IndexBuildOptions{Batch: 3, PurgeBatch: 4}

func mustCheck(t *testing.T, s *PGStore) {
	t.Helper()
	diff, err := s.CheckIndexEntries(t.Context(), testDB)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Empty() {
		t.Fatalf("index entries differ from the documents': %s", diff)
	}
}

func mustBuild(t *testing.T, s *PGStore) {
	t.Helper()
	if err := s.BuildIndexes(t.Context(), smallBuilds); err != nil {
		t.Fatal(err)
	}
}

func asc(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}}
}

func desc(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_DESCENDING}}
}

func contains(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}}
}

func compositeSpec(scope adminpb.Index_QueryScope, fields ...*adminpb.Index_IndexField) *adminpb.Index {
	return &adminpb.Index{QueryScope: scope, Fields: fields}
}

// indexIDOf returns the id in an index's name.
func indexIDOf(t *testing.T, name string) (string, int64) {
	t.Helper()
	_, id, _ := strings.Cut(name, "/indexes/")
	n, ok := ParseIndexID(id)
	if !ok {
		t.Fatalf("index name %q", name)
	}
	return id, n
}

func scanPaths(t *testing.T, s *PGStore, r IndexRange) []string {
	t.Helper()
	hits, _, err := s.ScanIndex(t.Context(), testDB, r)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path.String()
	}
	return out
}

// Commits keep the automatic single-field entries exact through creates, updates, masked
// updates and deletes, and a scan reads an index in order.
func TestCommitIndexEntries(t *testing.T) {
	s, _ := newStore(t, Options{})
	mustCommit(t, s,
		set("c/a", doc("n", value.Int(3), "t", value.Array(value.String("x"), value.String("y")))),
		set("c/b", doc("n", value.Int(1), "m", value.Map(value.Field{Name: "k", Value: value.Bool(true)}))),
		set("c/c", doc("n", value.String("s"))),
		set("c/a/c/z", doc("n", value.Int(0))),
		set("d/a", doc("n", value.Int(2))))
	mustCheck(t, s)
	id := SingleFieldIndexID("c", index.ScopeCollection, "n", index.Ascending)
	top := IndexKeyPrefix(index.ScopeCollection, nil, "c")
	if got := scanPaths(t, s, IndexRange{Index: id, Lo: top, Hi: IndexPrefixEnd(top)}); !slices.Equal(got, []string{"c/b", "c/a", "c/c"}) {
		t.Errorf("c by n: %v", got)
	}
	descID := SingleFieldIndexID("c", index.ScopeCollection, "n", index.Descending)
	if got := scanPaths(t, s, IndexRange{Index: descID, Lo: top, Hi: IndexPrefixEnd(top), Limit: 2}); !slices.Equal(got, []string{"c/c", "c/a"}) {
		t.Errorf("c by n descending, 2: %v", got)
	}
	lo := AppendIndexValue(cloneKey(top), value.Int(2), false)
	hi := AppendIndexValue(cloneKey(top), value.Int(10), false)
	if got := scanPaths(t, s, IndexRange{Index: id, Lo: lo, Hi: hi}); !slices.Equal(got, []string{"c/a"}) {
		t.Errorf("2 <= n < 10: %v", got)
	}
	tID := SingleFieldIndexID("c", index.ScopeCollection, "t", index.Contains)
	y := AppendIndexValue(cloneKey(top), value.String("y"), false)
	if got := scanPaths(t, s, IndexRange{Index: tID, Lo: y, Hi: IndexPrefixEnd(y)}); !slices.Equal(got, []string{"c/a"}) {
		t.Errorf("t contains y: %v", got)
	}

	mustCommit(t, s,
		Write{Kind: WriteUpdate, Path: ParsePath("c/a"), Fields: doc("n", value.Int(0)), Masked: true, Mask: []value.FieldPath{{"n"}}},
		Write{Kind: WriteDelete, Path: ParsePath("c/b")},
		set("c/c", doc("m", value.Map(value.Field{Name: "k", Value: value.Int(5)}))))
	mustCheck(t, s)
	if got := scanPaths(t, s, IndexRange{Index: id, Lo: top, Hi: IndexPrefixEnd(top)}); !slices.Equal(got, []string{"c/a"}) {
		t.Errorf("c by n after: %v", got)
	}
	// A REQUEST_TIME commit's entries hold its commit time.
	mustCommit(t, s, Write{Kind: WriteUpdate, Path: ParsePath("c/r"), Fields: value.Map(),
		Transforms: []Transform{{Path: value.FieldPath{"at"}, Kind: TransformServerTime}}})
	mustCheck(t, s)
}

func cloneKey(b []byte) []byte { return append([]byte(nil), b...) }

// The limits refuse the commit with production's error, before anything is written.
func TestCommitIndexLimits(t *testing.T) {
	s, _ := newStore(t, Options{})
	long := ParsePath("c/" + strings.Repeat("n", 6144-16-2-1))
	_, err := s.Commit(t.Context(), testDB, []Write{set(long.String(), value.Map())})
	wantError(t, err, InvalidArgument, "Index entry is too large.")
	els := make([]value.Value, 40_001)
	for i := range els {
		els[i] = value.Int(int64(i))
	}
	_, err = s.Commit(t.Context(), testDB, []Write{set("c/d", doc("t", value.Array(els...)))})
	wantError(t, err, InvalidArgument, ErrTooManyIndexEntries.Message)
	if _, _, err := s.Get(t.Context(), testDB, ParsePath("c/d"), ReadOptions{}); err == nil {
		t.Error("a refused commit wrote its document")
	}
	mustCheck(t, s)
}

// A composite index is CREATING, written by commits from its creation, filled by the build,
// then READY; its operation completes; the same index again is ALREADY_EXISTS; the catalog the
// planner reads follows each step.
func TestCompositeLifecycle(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	for i := range 10 {
		mustCommit(t, s, set(fmt.Sprintf("c/%02d", i), doc("a", value.Int(int64(i%3)), "b", value.String(fmt.Sprint(i)))))
	}
	op, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("b")))
	if err != nil {
		t.Fatal(err)
	}
	md := &adminpb.IndexOperationMetadata{}
	if err := op.GetMetadata().UnmarshalTo(md); err != nil || op.GetDone() || md.GetState() != adminpb.OperationState_PROCESSING {
		t.Fatalf("operation %v (%v)", op, err)
	}
	idStr, id := indexIDOf(t, md.GetIndex())
	ix, err := s.GetIndex(ctx, testDB, "c", idStr)
	if err != nil || ix.GetState() != adminpb.Index_CREATING || len(ix.GetFields()) != 3 || ix.GetFields()[2].GetOrder() != adminpb.Index_IndexField_DESCENDING {
		t.Fatalf("index %v (%v)", ix, err)
	}
	if _, ok, _ := s.CompositeIndexID(ctx, testDB, index.Index{CollectionGroup: "c", Scope: index.ScopeCollection,
		Fields: []index.IndexField{{Path: "a", Mode: index.Ascending}, {Path: "b", Mode: index.Descending}}}); ok {
		t.Error("a CREATING index is offered to the planner")
	}
	if _, _, err := s.ScanIndex(ctx, testDB, IndexRange{Index: id}); !errors.Is(err, errIndexNotReady) {
		t.Errorf("scan of a CREATING index: %v", err)
	}
	if _, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("b"), desc("__name__"))); err != errIndexExists {
		t.Errorf("the same index again: %v", err)
	}
	// Commits write it while it is built.
	mustCommit(t, s, set("c/10", doc("a", value.Int(1), "b", value.String("x"))), Write{Kind: WriteDelete, Path: ParsePath("c/00")})
	mustBuild(t, s)
	mustCheck(t, s)
	if ix, _ := s.GetIndex(ctx, testDB, "c", idStr); ix.GetState() != adminpb.Index_READY {
		t.Errorf("after the build: %v", ix.GetState())
	}
	got, ok, err := s.CompositeIndexID(ctx, testDB, index.Index{CollectionGroup: "c", Scope: index.ScopeCollection,
		Fields: []index.IndexField{{Path: "a", Mode: index.Ascending}, {Path: "b", Mode: index.Descending}}})
	if err != nil || !ok || got != id {
		t.Errorf("CompositeIndexID: %d %v %v", got, ok, err)
	}
	top := IndexKeyPrefix(index.ScopeCollection, nil, "c")
	a1 := AppendIndexValue(cloneKey(top), value.Int(1), false)
	if paths := scanPaths(t, s, IndexRange{Index: id, Lo: a1, Hi: IndexPrefixEnd(a1)}); !slices.Equal(paths, []string{"c/10", "c/07", "c/04", "c/01"}) {
		t.Errorf("a == 1 by b descending: %v", paths)
	}
	op, err = s.GetOperation(ctx, testDB, strings.TrimPrefix(op.GetName(), testDB.Name()+"/operations/"))
	if err != nil || !op.GetDone() || op.GetResponse() == nil {
		t.Fatalf("operation after: %v %v", op, err)
	}
	_ = op.GetMetadata().UnmarshalTo(md)
	if md.GetState() != adminpb.OperationState_SUCCESSFUL || md.GetEndTime() == nil {
		t.Errorf("metadata after: %v", md)
	}
	cat, err := s.IndexCatalog(ctx, testDB)
	if err != nil || cat == nil {
		t.Fatal(err)
	}
	list, err := s.ListIndexes(ctx, testDB, "-")
	if err != nil || len(list) != 1 || list[0].GetName() != testDB.IndexName("c", id) {
		t.Fatalf("list: %v %v", list, err)
	}

	// Deleted: out of the list and the catalog at once, its entries purged by the build, and
	// the same index may be created again before that.
	if err := s.DeleteIndex(ctx, testDB, "c", idStr); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetIndex(ctx, testDB, "c", idStr); err != errIndexNotFound {
		t.Errorf("a deleted index: %v", err)
	}
	if err := s.DeleteIndex(ctx, testDB, "c", idStr); err != errIndexNotFound {
		t.Errorf("deleted twice: %v", err)
	}
	op2, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("b")))
	if err != nil {
		t.Fatalf("recreate before the purge: %v", err)
	}
	mustBuild(t, s)
	mustCheck(t, s)
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents.index_entries WHERE index_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d entries of the deleted index left (%v)", n, err)
	}
	_ = op2
}

// What CreateIndex refuses, and how it normalizes.
func TestCreateIndexValidation(t *testing.T) {
	s, _ := newStore(t, Options{IndexLimits: IndexLimits{Composites: 2}})
	ctx := t.Context()
	vector := &adminpb.Index_IndexField{FieldPath: "v", ValueMode: &adminpb.Index_IndexField_VectorConfig_{
		VectorConfig: &adminpb.Index_IndexField_VectorConfig{Dimension: 3, Type: &adminpb.Index_IndexField_VectorConfig_Flat{Flat: &adminpb.Index_IndexField_VectorConfig_FlatIndex{}}}}}
	for _, c := range []struct {
		name string
		ix   *adminpb.Index
		code Code
	}{
		{"one field", compositeSpec(adminpb.Index_COLLECTION, asc("a")), InvalidArgument},
		{"one field and its __name__", compositeSpec(adminpb.Index_COLLECTION, desc("a"), desc("__name__")), InvalidArgument},
		{"contains alone", compositeSpec(adminpb.Index_COLLECTION, contains("a")), InvalidArgument},
		{"no scope", compositeSpec(adminpb.Index_QUERY_SCOPE_UNSPECIFIED, asc("a"), asc("b")), InvalidArgument},
		{"twice", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("a")), InvalidArgument},
		{"two contains", compositeSpec(adminpb.Index_COLLECTION, contains("a"), contains("b")), InvalidArgument},
		{"__name__ first", compositeSpec(adminpb.Index_COLLECTION, asc("__name__"), asc("a")), InvalidArgument},
		{"bad path", compositeSpec(adminpb.Index_COLLECTION, asc("a b"), asc("c")), InvalidArgument},
		{"no mode", compositeSpec(adminpb.Index_COLLECTION, &adminpb.Index_IndexField{FieldPath: "a"}, asc("b")), InvalidArgument},
	} {
		_, err := s.CreateIndex(ctx, testDB, "c", c.ix)
		if e, ok := errors.AsType[*Error](err); !ok || e.Code != c.code {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// One field and __name__ the other way is necessary (R5); so is a collection-group one.
	if _, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("__name__"))); err != nil {
		t.Errorf("a ascending, __name__ descending: %v", err)
	}
	// A vector index is READY at once, its operation done.
	op, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("k"), vector))
	if err != nil || !op.GetDone() {
		t.Fatalf("vector: %v %v", op, err)
	}
	_, err = s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION_GROUP, asc("a")))
	wantError(t, err, ResourceExhausted, "The database has the most composite indexes allowed (2).")
	list, _ := s.ListIndexes(ctx, testDB, "c")
	if len(list) != 2 || list[1].GetState() != adminpb.Index_READY {
		t.Errorf("list: %v", list)
	}
}

// normalizeComposite puts __name__ where firebase-tools 15 expects it (processIndex): last, or
// before a vector field, in the direction of the last ordered field; and takes an exported
// file's density back.
func TestNormalizeComposite(t *testing.T) {
	vector := &adminpb.Index_IndexField{FieldPath: "v", ValueMode: &adminpb.Index_IndexField_VectorConfig_{
		VectorConfig: &adminpb.Index_IndexField_VectorConfig{Dimension: 3, Type: &adminpb.Index_IndexField_VectorConfig_Flat{Flat: &adminpb.Index_IndexField_VectorConfig_FlatIndex{}}}}}
	show := func(ix *adminpb.Index) string {
		var parts []string
		for _, f := range ix.GetFields() {
			mode := f.GetOrder().String()
			switch {
			case f.GetVectorConfig() != nil:
				mode = "VECTOR"
			case f.GetArrayConfig() != adminpb.Index_IndexField_ARRAY_CONFIG_UNSPECIFIED:
				mode = "CONTAINS"
			}
			parts = append(parts, f.GetFieldPath()+" "+mode)
		}
		return strings.Join(parts, ", ")
	}
	for _, c := range []struct {
		ix   *adminpb.Index
		want string
	}{
		{compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("b")), "a ASCENDING, b DESCENDING, __name__ DESCENDING"},
		{compositeSpec(adminpb.Index_COLLECTION, desc("a"), contains("t")), "a DESCENDING, t CONTAINS, __name__ DESCENDING"},
		{compositeSpec(adminpb.Index_COLLECTION, contains("t"), desc("a")), "t CONTAINS, a DESCENDING, __name__ DESCENDING"},
		{compositeSpec(adminpb.Index_COLLECTION, desc("k"), vector), "k DESCENDING, __name__ DESCENDING, v VECTOR"},
		{compositeSpec(adminpb.Index_COLLECTION, vector), "__name__ ASCENDING, v VECTOR"},
		{compositeSpec(adminpb.Index_COLLECTION, asc("k"), asc("__name__"), vector), "k ASCENDING, __name__ ASCENDING, v VECTOR"},
		{&adminpb.Index{QueryScope: adminpb.Index_COLLECTION, Density: adminpb.Index_SPARSE_ALL, Fields: []*adminpb.Index_IndexField{asc("a"), asc("b")}},
			"a ASCENDING, b ASCENDING, __name__ ASCENDING"},
	} {
		got, err := normalizeComposite(c.ix)
		if err != nil || show(got) != c.want || got.GetDensity() != adminpb.Index_DENSITY_UNSPECIFIED {
			t.Errorf("%s: %s %v, want %s", show(c.ix), show(got), err, c.want)
		}
	}
	for _, ix := range []*adminpb.Index{
		compositeSpec(adminpb.Index_COLLECTION, vector, asc("a")),
		compositeSpec(adminpb.Index_COLLECTION, contains("t"), vector),
		{QueryScope: adminpb.Index_COLLECTION, Density: adminpb.Index_DENSE, Fields: []*adminpb.Index_IndexField{asc("a"), asc("b")}},
		{QueryScope: adminpb.Index_COLLECTION, Unique: true, Fields: []*adminpb.Index_IndexField{asc("a"), asc("b")}},
	} {
		if _, err := normalizeComposite(ix); err == nil {
			t.Errorf("%v: accepted", ix)
		}
	}
}

// Field updates: an exemption removes a field's entries, its removal restores them, a
// collection-group override adds them across collections of the group, and each change shows in
// the Field resource with its inheritance.
func TestFieldOverrides(t *testing.T) {
	s, _ := newStore(t, Options{IndexLimits: IndexLimits{FieldOverrides: 2}})
	ctx := t.Context()
	for i := range 7 {
		mustCommit(t, s, set(fmt.Sprintf("p/%d/c/d", i), doc("a", value.Int(int64(i)), "m", value.Map(value.Field{Name: "x", Value: value.Int(1)}))))
		mustCommit(t, s, set(fmt.Sprintf("c/%d", i), doc("a", value.Int(int64(i)))))
	}
	update := func(group, path string, cfg *adminpb.Field_IndexConfig, mask ...string) {
		t.Helper()
		op, err := s.UpdateField(ctx, testDB, group, &adminpb.Field{Name: testDB.FieldName(group, path), IndexConfig: cfg}, mask)
		if err != nil {
			t.Fatal(err)
		}
		if op.GetDone() {
			t.Error("a field update is done before the build")
		}
		mustBuild(t, s)
		mustCheck(t, s)
		id := strings.TrimPrefix(op.GetName(), testDB.Name()+"/operations/")
		if op, err = s.GetOperation(ctx, testDB, id); err != nil || !op.GetDone() {
			t.Fatalf("operation %v %v", op, err)
		}
	}
	countIndex := func(id int64) int {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM documents.index_entries WHERE index_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	aAsc := SingleFieldIndexID("c", index.ScopeCollection, "a", index.Ascending)
	if n := countIndex(aAsc); n != 14 {
		t.Fatalf("%d entries of c.a ascending", n)
	}
	// Exempt c.a: none left.
	update("c", "a", &adminpb.Field_IndexConfig{}, "indexConfig")
	if n := countIndex(aAsc); n != 0 {
		t.Errorf("exempted: %d entries", n)
	}
	f, err := s.GetField(ctx, testDB, "c", "a")
	if err != nil || len(f.GetIndexConfig().GetIndexes()) != 0 || f.GetIndexConfig().GetUsesAncestorConfig() {
		t.Errorf("exempted field: %v %v", f, err)
	}
	// Enable collection-group ascending on c.m (and so m.x), keep the rest.
	cg := &adminpb.Field_IndexConfig{Indexes: fieldIndexProtos("m", []index.SingleFieldIndex{
		{Scope: index.ScopeCollection, Mode: index.Ascending}, {Scope: index.ScopeCollectionGroup, Mode: index.Ascending}})}
	update("c", "m", cg)
	cgID := SingleFieldIndexID("c", index.ScopeCollectionGroup, "m.x", index.Ascending)
	if n := countIndex(cgID); n != 7 {
		t.Errorf("collection-group m.x: %d entries, want 7", n)
	}
	if got := scanPaths(t, s, IndexRange{Index: cgID, Limit: 3}); !slices.Equal(got, []string{"p/0/c/d", "p/1/c/d", "p/2/c/d"}) {
		t.Errorf("collection-group scan: %v", got)
	}
	sub, _ := s.GetField(ctx, testDB, "c", "m.x")
	if !sub.GetIndexConfig().GetUsesAncestorConfig() || sub.GetIndexConfig().GetAncestorField() != testDB.FieldName("c", "m") ||
		len(sub.GetIndexConfig().GetIndexes()) != 2 {
		t.Errorf("m.x inherits: %v", sub)
	}
	// The cap counts explicit overrides.
	_, err = s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "z"), IndexConfig: &adminpb.Field_IndexConfig{}}, nil)
	wantError(t, err, ResourceExhausted, "The database has the most field overrides allowed (2).")
	// Revert c.a ({} with no mask): its automatic indexes come back.
	update("c", "a", nil)
	if n := countIndex(aAsc); n != 14 {
		t.Errorf("reverted: %d entries", n)
	}
	f, _ = s.GetField(ctx, testDB, "c", "a")
	if !f.GetIndexConfig().GetUsesAncestorConfig() || f.GetIndexConfig().GetAncestorField() != testDB.FieldName(index.DefaultCollectionGroup, "*") {
		t.Errorf("reverted field: %v", f)
	}
	fields, err := s.ListFields(ctx, testDB, "-")
	if err != nil || len(fields) != 2 || fields[0].GetName() != testDB.FieldName(index.DefaultCollectionGroup, "*") ||
		fields[1].GetName() != testDB.FieldName("c", "m") {
		t.Errorf("list: %v %v", fields, err)
	}
	// A TTL policy alone builds nothing.
	op, err := s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "m"), TtlConfig: &adminpb.Field_TtlConfig{}}, []string{"ttlConfig"})
	if err != nil {
		t.Fatal(err)
	}
	mustBuild(t, s)
	f, _ = s.GetField(ctx, testDB, "c", "m")
	if f.GetTtlConfig().GetState() != adminpb.Field_TtlConfig_ACTIVE || len(f.GetIndexConfig().GetIndexes()) != 2 {
		t.Errorf("ttl: %v", f)
	}
	_ = op
	// A database-wide exemption: no single-field entry at all.
	update(index.DefaultCollectionGroup, "*", &adminpb.Field_IndexConfig{})
	var n int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM documents.index_entries WHERE index_id < 0 AND index_id <> $1`, cgID).Scan(&n)
	if n != 7*3 { // c.m's own, but for m.x's collection-group one: m's two, m.x's collection ascending
		t.Errorf("after the default's exemption: %d entries", n)
	}
}

// The catalog cache follows every change at once, and a commit that computed its entries with
// a catalog that changed under it is computed again.
func TestCatalogInvalidation(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	mustCommit(t, s, set("c/a", doc("a", value.Int(1), "b", value.Int(2))))
	before, err := s.cats.get(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), asc("b"))); err != nil {
		t.Fatal(err)
	}
	after, _ := s.cats.get(ctx, testDB)
	if after == before || after.version != before.version+1 || len(after.composites) != 1 {
		t.Fatalf("catalog after CreateIndex: version %d → %d", before.version, after.version)
	}
	// Another store (another process's cache, stale): its commit is computed again and writes
	// the new index.
	other := NewPGStore(s.pool, Options{})
	other.cats.m[dbKey{testDB.Project, testDB.ID}] = before
	mustCommit(t, other, set("c/b", doc("a", value.Int(1), "b", value.Int(3))))
	mustBuild(t, s)
	mustCheck(t, s)
	cat, _ := s.IndexCatalog(ctx, testDB)
	if cat == nil {
		t.Fatal("no planner catalog")
	}
}

// A build stopped part way, as by a restart, resumes from its cursor in another store.
func TestBuildResumesAfterRestart(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	var ws []Write
	for i := range 20 {
		ws = append(ws, set(fmt.Sprintf("c/%02d", i), doc("a", value.Int(int64(i)), "b", value.Bool(i%2 == 0))))
	}
	mustCommit(t, s, ws...)
	if _, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("b"), desc("a"))); err != nil {
		t.Fatal(err)
	}
	o := smallBuilds
	o.maxBatches = 3
	if err := s.BuildIndexes(ctx, o); !errors.Is(err, errBuildStopped) {
		t.Fatalf("stopped build: %v", err)
	}
	var cursor []byte
	if err := pool.QueryRow(ctx, `SELECT build_cursor FROM documents.indexes`).Scan(&cursor); err != nil || cursor == nil {
		t.Fatalf("no cursor after 3 batches: %v", err)
	}
	mustCommit(t, s, set("c/00", doc("a", value.Int(-1), "b", value.Bool(true))), Write{Kind: WriteDelete, Path: ParsePath("c/19")})
	restarted := NewPGStore(pool, Options{})
	mustBuild(t, restarted)
	mustCheck(t, restarted)
}

// The build and commits run together: a composite created, a field exempted, a collection
// group enabled, while writers create, update and delete documents of the group under two
// parents; afterwards the entries are exactly the documents'.
func TestBuildsUnderConcurrentCommits(t *testing.T) {
	s, _ := newStore(t, Options{CommitConcurrency: 4})
	ctx := t.Context()
	names := make([]string, 0, 40)
	for i := range 20 {
		names = append(names, fmt.Sprintf("c/%02d", i), fmt.Sprintf("p/x/c/%02d", i))
	}
	randDoc := func(r *rand.Rand) value.Value {
		tags := make([]value.Value, r.IntN(4))
		for i := range tags {
			tags[i] = value.String(fmt.Sprint(r.IntN(5)))
		}
		return doc("a", value.Int(int64(r.IntN(5))), "b", value.String(fmt.Sprint(r.IntN(100))), "t", value.Array(tags...),
			"m", value.Map(value.Field{Name: "x", Value: value.Int(int64(r.IntN(3)))}))
	}
	r0 := rand.New(rand.NewPCG(7, 7))
	for _, n := range names {
		mustCommit(t, s, set(n, randDoc(r0)))
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var commits atomic.Int64
	for w := range 4 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 1))
			for {
				select {
				case <-stop:
					return
				default:
				}
				var ws []Write
				for range 1 + r.IntN(3) {
					n := names[r.IntN(len(names))]
					switch r.IntN(4) {
					case 0:
						ws = append(ws, Write{Kind: WriteDelete, Path: ParsePath(n)})
					case 1:
						ws = append(ws, Write{Kind: WriteUpdate, Path: ParsePath(n), Fields: doc("a", value.Int(int64(r.IntN(5)))),
							Masked: true, Mask: []value.FieldPath{{"a"}}})
					default:
						ws = append(ws, set(n, randDoc(r)))
					}
				}
				if _, err := s.Commit(ctx, testDB, ws); err != nil && !errors.Is(err, ErrContention) {
					t.Error(err)
					return
				}
				commits.Add(1)
			}
		})
	}
	run := func(f func() error) {
		t.Helper()
		if err := f(); err != nil {
			t.Fatal(err)
		}
		if err := s.BuildIndexes(ctx, IndexBuildOptions{Batch: 2, PurgeBatch: 3}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var first string
	run(func() error {
		op, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), desc("b")))
		if err == nil {
			md := &adminpb.IndexOperationMetadata{}
			_ = op.GetMetadata().UnmarshalTo(md)
			first, _ = indexIDOf(t, md.GetIndex())
		}
		return err
	})
	run(func() error {
		_, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION_GROUP, contains("t"), asc("a")))
		return err
	})
	run(func() error {
		_, err := s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "b"), IndexConfig: &adminpb.Field_IndexConfig{}}, nil)
		return err
	})
	run(func() error {
		_, err := s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "m"), IndexConfig: &adminpb.Field_IndexConfig{
			Indexes: fieldIndexProtos("m", []index.SingleFieldIndex{{Scope: index.ScopeCollectionGroup, Mode: index.Descending}})}}, nil)
		return err
	})
	run(func() error { return s.DeleteIndex(ctx, testDB, "c", first) })
	run(func() error {
		_, err := s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "b")}, nil)
		return err
	})
	close(stop)
	wg.Wait()
	mustBuild(t, s)
	mustCheck(t, s)
	if commits.Load() < 20 {
		t.Errorf("only %d commits ran during the builds", commits.Load())
	}
	t.Logf("%d commits during the builds", commits.Load())
}

// Purging a project removes its index entries, indexes, overrides and operations.
func TestPurgeIndexes(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	mustCommit(t, s, set("c/a", doc("a", value.Int(1), "b", value.Int(1))))
	if _, err := s.CreateIndex(ctx, testDB, "c", compositeSpec(adminpb.Index_COLLECTION, asc("a"), asc("b"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateField(ctx, testDB, "c", &adminpb.Field{Name: testDB.FieldName("c", "a"), IndexConfig: &adminpb.Field_IndexConfig{}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := PurgeProject(ctx, pool, testDB.Project); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"index_entries", "indexes", "field_overrides", "operations"} {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM documents.`+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows left (%v)", table, n, err)
		}
	}
}
