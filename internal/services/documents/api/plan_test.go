package api_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The planner's consistency tests (ADR-025 §1): every query runs through the planner and through
// the reference path alone (apitest.Env.Reference), which must answer the same documents in the
// same order, or the same error; and the path counters prove each index path ran.

const docsRoot = "projects/" + apitest.Project + "/databases/(default)/documents"

// planEnv is an environment with enforcement off, so the planner sees every shape.
type planEnv struct {
	env  *apitest.Env
	db   core.DB
	call api.Call
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	env := apitest.New(t, apitest.Options{})
	p := &planEnv{env: env, db: core.DB{Project: env.Number, ProjectID: apitest.Project, ID: core.DefaultDatabase},
		call: api.Call{Authorization: "Bearer " + env.JWT(t), Transport: authn.Transport{Peer: netip.MustParseAddr("127.0.0.1")}}}
	if _, err := env.Store.Database(t.Context(), p.db); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Pool.Exec(t.Context(), `UPDATE documents.databases SET index_enforcement = 'off' WHERE project_number = $1`,
		env.Number); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *planEnv) commit(t *testing.T, writes ...core.Write) {
	t.Helper()
	if _, err := p.env.Store.Commit(t.Context(), p.db, writes); err != nil {
		t.Fatal(err)
	}
}

func setDoc(path string, fields value.Value) core.Write {
	return core.Write{Kind: core.WriteUpdate, Path: core.ParsePath(path), Fields: fields}
}

func ascField(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}}
}

func descField(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_DESCENDING}}
}

func containsField(path string) *adminpb.Index_IndexField {
	return &adminpb.Index_IndexField{FieldPath: path, ValueMode: &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}}
}

// createIndex creates a composite index and builds it to READY.
func (p *planEnv) createIndex(t *testing.T, group string, scope adminpb.Index_QueryScope, fields ...*adminpb.Index_IndexField) {
	t.Helper()
	if _, err := p.env.Store.CreateIndex(t.Context(), p.db, group, &adminpb.Index{QueryScope: scope, Fields: fields}); err != nil {
		t.Fatal(err)
	}
	p.build(t)
}

func (p *planEnv) build(t *testing.T) {
	t.Helper()
	if err := p.env.Store.BuildIndexes(t.Context(), core.IndexBuildOptions{Batch: 7}); err != nil {
		t.Fatal(err)
	}
}

// groupOverride enables a field's collection-group single-field indexes beside its automatic
// ones.
func (p *planEnv) groupOverride(t *testing.T, group, path string) {
	t.Helper()
	ix := func(scope adminpb.Index_QueryScope, f *adminpb.Index_IndexField) *adminpb.Index {
		return &adminpb.Index{QueryScope: scope, Fields: []*adminpb.Index_IndexField{f}}
	}
	cfg := &adminpb.Field_IndexConfig{Indexes: []*adminpb.Index{
		ix(adminpb.Index_COLLECTION, ascField(path)), ix(adminpb.Index_COLLECTION, descField(path)),
		ix(adminpb.Index_COLLECTION, containsField(path)),
		ix(adminpb.Index_COLLECTION_GROUP, ascField(path)), ix(adminpb.Index_COLLECTION_GROUP, descField(path)),
		ix(adminpb.Index_COLLECTION_GROUP, containsField(path)),
	}}
	if _, err := p.env.Store.UpdateField(t.Context(), p.db, group, &adminpb.Field{Name: p.db.FieldName(group, path), IndexConfig: cfg},
		[]string{"indexConfig"}); err != nil {
		t.Fatal(err)
	}
	p.build(t)
}

// answer is a query's answer: its documents in order and the results the offset skipped, an
// aggregation's result, or its error.
type answer struct {
	docs    []*pb.Document
	skipped int32
	agg     *pb.AggregationResult
	err     error
}

func (a answer) equal(b answer) bool {
	if a.err != nil || b.err != nil {
		return a.err != nil && b.err != nil && a.err.Error() == b.err.Error()
	}
	if len(a.docs) != len(b.docs) || a.skipped != b.skipped || !proto.Equal(a.agg, b.agg) {
		return false
	}
	for i := range a.docs {
		if !proto.Equal(a.docs[i], b.docs[i]) {
			return false
		}
	}
	return true
}

func (a answer) String() string {
	if a.err != nil {
		return "error " + a.err.Error()
	}
	names := make([]string, len(a.docs))
	for i, d := range a.docs {
		n := strings.TrimPrefix(d.GetName(), docsRoot+"/")
		if len(n) > 40 {
			n = n[:20] + "…" + n[len(n)-20:]
		}
		names[i] = n
	}
	return fmt.Sprintf("skipped %d %v agg %v", a.skipped, names, a.agg)
}

func (p *planEnv) run(ctx context.Context, svc *api.Service, sq *pb.StructuredQuery, aggs []*pb.StructuredAggregationQuery_Aggregation) answer {
	var out answer
	if aggs != nil {
		req := &pb.RunAggregationQueryRequest{Parent: docsRoot, QueryType: &pb.RunAggregationQueryRequest_StructuredAggregationQuery{
			StructuredAggregationQuery: &pb.StructuredAggregationQuery{
				QueryType:    &pb.StructuredAggregationQuery_StructuredQuery{StructuredQuery: sq},
				Aggregations: aggs}}}
		out.err = svc.RunAggregationQuery(ctx, p.call, req, func(r *pb.RunAggregationQueryResponse) error {
			if r.GetResult() != nil {
				out.agg = r.GetResult()
			}
			return nil
		})
		return out
	}
	req := &pb.RunQueryRequest{Parent: docsRoot, QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: sq}}
	first := true
	out.err = svc.RunQuery(ctx, p.call, req, func(r *pb.RunQueryResponse) error {
		if first {
			out.skipped, first = r.GetSkippedResults(), false
		}
		if r.GetDocument() != nil {
			out.docs = append(out.docs, r.GetDocument())
		}
		return nil
	})
	return out
}

// indexServed is how many queries the index paths served.
func indexServed() int64 {
	return query.ServedCount(query.PathIndex) + query.ServedCount(query.PathIndexMerged) + query.ServedCount(query.PathIndexContains)
}

// fieldFilter, unary, and, or, order and from build StructuredQuery parts.
func fieldFilter(path string, op pb.StructuredQuery_FieldFilter_Operator, v value.Value) *pb.StructuredQuery_Filter {
	return &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
		Field: &pb.StructuredQuery_FieldReference{FieldPath: path}, Op: op, Value: wire.ValueToProto(v)}}}
}

func unary(path string, op pb.StructuredQuery_UnaryFilter_Operator) *pb.StructuredQuery_Filter {
	return &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_UnaryFilter{UnaryFilter: &pb.StructuredQuery_UnaryFilter{
		Op: op, OperandType: &pb.StructuredQuery_UnaryFilter_Field{Field: &pb.StructuredQuery_FieldReference{FieldPath: path}}}}}
}

func composite(op pb.StructuredQuery_CompositeFilter_Operator, fs ...*pb.StructuredQuery_Filter) *pb.StructuredQuery_Filter {
	return &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_CompositeFilter{CompositeFilter: &pb.StructuredQuery_CompositeFilter{
		Op: op, Filters: fs}}}
}

func order(path string, desc bool) *pb.StructuredQuery_Order {
	d := pb.StructuredQuery_ASCENDING
	if desc {
		d = pb.StructuredQuery_DESCENDING
	}
	return &pb.StructuredQuery_Order{Field: &pb.StructuredQuery_FieldReference{FieldPath: path}, Direction: d}
}

func from(id string, group bool) []*pb.StructuredQuery_CollectionSelector {
	return []*pb.StructuredQuery_CollectionSelector{{CollectionId: id, AllDescendants: group}}
}

// gen generates documents and queries over a few fields of mixed types.
type gen struct {
	r     *rand.Rand
	names []string // the documents written, for cursors on __name__
}

var (
	long1 = strings.Repeat("L", 1600)
	long2 = strings.Repeat("L", 1499)
)

// scalar is a value of one of the types a field holds.
func (g *gen) scalar() value.Value {
	switch g.r.IntN(16) {
	case 0:
		return value.Null()
	case 1:
		return value.Double(math.NaN())
	case 2:
		return value.Double(1.5)
	case 3:
		return value.Double(2)
	case 4:
		return value.Bool(g.r.IntN(2) == 0)
	case 5:
		return value.String([]string{"x", "y", long1 + "a", long1 + "b", long2, long2 + "é"}[g.r.IntN(6)])
	case 6:
		return value.Array(value.Int(int64(g.r.IntN(3))))
	case 7:
		return value.Map(value.Field{Name: "k", Value: value.Int(int64(g.r.IntN(2)))})
	case 8:
		return value.Bytes([]byte{0x00, 0xFF, byte(g.r.IntN(3))})
	}
	return value.Int(int64(g.r.IntN(4)))
}

func (g *gen) tags() value.Value {
	pool := []value.Value{value.String("red"), value.String("blue"), value.String("green"), value.Int(1), value.Double(1),
		value.Array(), value.Null(), value.Double(math.NaN()), value.String(long1)}
	n := g.r.IntN(4)
	els := make([]value.Value, n)
	for i := range els {
		els[i] = pool[g.r.IntN(len(pool))]
	}
	return value.Array(els...)
}

func (g *gen) fields() value.Value {
	var fs []value.Field
	add := func(name string, v value.Value) {
		if g.r.IntN(6) > 0 {
			fs = append(fs, value.Field{Name: name, Value: v})
		}
	}
	add("a", g.scalar())
	add("b", g.scalar())
	add("c", value.String([]string{long1 + "a", long1 + "b", long1 + "a", long2, "s"}[g.r.IntN(5)]))
	add("tags", g.tags())
	add("m", value.Map(value.Field{Name: "x", Value: value.Int(int64(g.r.IntN(3)))}))
	return value.Map(fs...)
}

// path is a document path: in collection c, or in group g under c's documents and under p/x,
// with ids short or long, so some names pass 2,048 bytes and share long prefixes. The long ids
// keep a composite entry of two 1,500-byte strings within production's entry size
// (core/indexentries.go): 2·D + P at most about 5,100 for a name of size D and its parent's P.
func (g *gen) path() string {
	id := func(long int) string {
		if g.r.IntN(3) == 0 {
			return strings.Repeat("n", long) + fmt.Sprintf("%03d", g.r.IntN(40))
		}
		return fmt.Sprintf("d%02d", g.r.IntN(40))
	}
	switch g.r.IntN(4) {
	case 0:
		return "c/" + id(900) + "/g/" + id(1137)
	case 1:
		return "p/x/g/" + id(1137)
	}
	return "c/" + id(900)
}

var (
	fieldPaths = []string{"a", "b", "c", "tags", "m.x", "a", "b"}
	rangeOps   = []pb.StructuredQuery_FieldFilter_Operator{
		pb.StructuredQuery_FieldFilter_LESS_THAN, pb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL,
		pb.StructuredQuery_FieldFilter_GREATER_THAN, pb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL,
	}
)

func (g *gen) operand(path string) value.Value {
	switch path {
	case "c":
		return value.String([]string{long1 + "a", long2, "s", "t"}[g.r.IntN(4)])
	case "m.x":
		return value.Int(int64(g.r.IntN(3)))
	case "tags":
		return []value.Value{value.String("red"), value.String("blue"), value.Int(1), value.Array()}[g.r.IntN(4)]
	}
	v := g.scalar()
	for v.IsNull() || v.IsNaN() {
		v = g.scalar()
	}
	return v
}

func (g *gen) values(path string) value.Value {
	n := 1 + g.r.IntN(3)
	vs := make([]value.Value, n)
	for i := range vs {
		vs[i] = g.operand(path)
	}
	return value.Array(vs...)
}

func (g *gen) leaf() *pb.StructuredQuery_Filter {
	path := fieldPaths[g.r.IntN(len(fieldPaths))]
	name := func() value.Value { return value.Reference(docsRoot + "/" + g.names[g.r.IntN(len(g.names))]) }
	switch k := g.r.IntN(22); {
	case k == 20:
		return fieldFilter("__name__", pb.StructuredQuery_FieldFilter_EQUAL, name())
	case k == 21:
		return fieldFilter("__name__", pb.StructuredQuery_FieldFilter_IN, value.Array(name(), name(), name()))
	case k < 6:
		return fieldFilter(path, pb.StructuredQuery_FieldFilter_EQUAL, g.operand(path))
	case k < 10:
		return fieldFilter(path, rangeOps[g.r.IntN(len(rangeOps))], g.operand(path))
	case k < 12:
		return fieldFilter(path, pb.StructuredQuery_FieldFilter_IN, g.values(path))
	case k < 14:
		return fieldFilter("tags", pb.StructuredQuery_FieldFilter_ARRAY_CONTAINS, g.operand("tags"))
	case k < 15:
		return fieldFilter("tags", pb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY, g.values("tags"))
	case k < 16:
		return fieldFilter(path, pb.StructuredQuery_FieldFilter_NOT_EQUAL, g.operand(path))
	case k < 17:
		return fieldFilter(path, pb.StructuredQuery_FieldFilter_NOT_IN, g.values(path))
	case k < 18:
		return unary(path, pb.StructuredQuery_UnaryFilter_IS_NULL)
	case k < 19:
		return unary(path, pb.StructuredQuery_UnaryFilter_IS_NAN)
	}
	return unary(path, pb.StructuredQuery_UnaryFilter_IS_NOT_NULL)
}

func (g *gen) where() *pb.StructuredQuery_Filter {
	switch g.r.IntN(6) {
	case 0:
		return nil
	case 1, 2:
		return g.leaf()
	case 3, 4:
		return composite(pb.StructuredQuery_CompositeFilter_AND, g.leaf(), g.leaf())
	}
	return composite(pb.StructuredQuery_CompositeFilter_OR, g.leaf(),
		composite(pb.StructuredQuery_CompositeFilter_AND, g.leaf(), g.leaf()))
}

func (g *gen) query() *pb.StructuredQuery {
	group := g.r.IntN(4) == 0
	id := "c"
	if group {
		id = "g"
	}
	sq := &pb.StructuredQuery{From: from(id, group), Where: g.where()}
	for range g.r.IntN(3) {
		path := []string{"a", "b", "c", "m.x", "__name__"}[g.r.IntN(5)]
		sq.OrderBy = append(sq.OrderBy, order(path, g.r.IntN(2) == 0))
	}
	cursor := func() *pb.Cursor {
		c := &pb.Cursor{Before: g.r.IntN(2) == 0}
		for range 1 + g.r.IntN(2) {
			c.Values = append(c.Values, wire.ValueToProto(g.operand("a")))
		}
		if g.r.IntN(2) == 0 && len(g.names) > 0 {
			// Ends with a name: a cursor on the whole order of a query ordered by one field.
			c.Values = []*pb.Value{wire.ValueToProto(g.operand("a")),
				wire.ValueToProto(value.Reference(docsRoot + "/" + g.names[g.r.IntN(len(g.names))]))}
		}
		return c
	}
	if g.r.IntN(4) == 0 {
		sq.StartAt = cursor()
	}
	if g.r.IntN(5) == 0 {
		sq.EndAt = cursor()
	}
	if g.r.IntN(3) == 0 {
		sq.Offset = int32(g.r.IntN(3))
	}
	if g.r.IntN(2) == 0 {
		sq.Limit = wrapperspb.Int32(int32(g.r.IntN(8)))
	}
	return sq
}

// differentialSetup writes random documents, builds the differential tests' indexes and
// overrides, then updates and deletes some: the generator's seed is ARUMA_PLAN_SEED, else 1.
func differentialSetup(t *testing.T) (*planEnv, *gen) {
	t.Helper()
	p := newPlanEnv(t)
	seed := uint64(1)
	if s, err := strconv.ParseUint(os.Getenv("ARUMA_PLAN_SEED"), 10, 64); err == nil {
		seed = s
	}
	t.Logf("seed %d (ARUMA_PLAN_SEED)", seed)
	g := &gen{r: rand.New(rand.NewPCG(seed, seed))}
	write := func(n int) {
		for range n {
			path := g.path()
			p.commit(t, setDoc(path, g.fields()))
			g.names = append(g.names, path)
		}
	}
	write(120)
	p.createIndex(t, "c", adminpb.Index_COLLECTION, ascField("a"), ascField("b"))
	p.createIndex(t, "c", adminpb.Index_COLLECTION, ascField("a"), descField("b"))
	p.createIndex(t, "c", adminpb.Index_COLLECTION, containsField("tags"), ascField("a"))
	p.createIndex(t, "c", adminpb.Index_COLLECTION, ascField("c"), ascField("a"))
	p.createIndex(t, "c", adminpb.Index_COLLECTION, descField("c"), descField("b"))
	p.createIndex(t, "g", adminpb.Index_COLLECTION_GROUP, ascField("a"), ascField("b"))
	p.createIndex(t, "g", adminpb.Index_COLLECTION_GROUP, ascField("a"), descField("m.x"))
	p.groupOverride(t, "g", "a")
	p.groupOverride(t, "g", "b")
	write(80)
	// Updates and deletes after the builds.
	for i := range 30 {
		w := setDoc(g.names[i*3], g.fields())
		if i%4 == 0 {
			w = core.Write{Kind: core.WriteDelete, Path: core.ParsePath(g.names[i*3])}
		}
		p.commit(t, w)
	}
	return p, g
}

// TestPlannerDifferential runs random queries over random documents, through the planner and the
// reference path alone, with composite indexes and collection-group overrides, and requires the
// same answers.
func TestPlannerDifferential(t *testing.T) {
	p, g := differentialSetup(t)
	ctx := t.Context()
	mismatches, served := query.MismatchCount(), indexServed()
	n := 1500
	if testing.Short() {
		n = 300
	}
	counts := map[string]int{}
	for i := range n {
		sq := g.query()
		var aggs []*pb.StructuredAggregationQuery_Aggregation
		if i%7 == 0 {
			aggs = []*pb.StructuredAggregationQuery_Aggregation{
				{Alias: "n", Operator: &pb.StructuredAggregationQuery_Aggregation_Count_{Count: &pb.StructuredAggregationQuery_Aggregation_Count{}}},
				{Alias: "s", Operator: &pb.StructuredAggregationQuery_Aggregation_Sum_{Sum: &pb.StructuredAggregationQuery_Aggregation_Sum{
					Field: &pb.StructuredQuery_FieldReference{FieldPath: "m.x"}}}},
			}
		}
		before := indexServed()
		got := p.run(ctx, p.env.Service, sq, aggs)
		want := p.run(ctx, p.env.Reference, sq, aggs)
		switch {
		case query.MismatchCount() != mismatches:
			t.Fatalf("query %d %v: a recheck mismatch (planner %v, reference %v)", i, sq, got, want)
		case !got.equal(want):
			t.Errorf("query %d %v:\nplanner   %v\nreference %v", i, sq, got, want)
		case want.err != nil:
			counts["error"]++
		case indexServed() > before:
			counts["index"]++
		default:
			counts["reference"]++
		}
	}
	t.Logf("%d queries: %v; %d served by an index path", n, counts, indexServed()-served)
	if got := query.MismatchCount() - mismatches; got != 0 {
		t.Errorf("%d recheck mismatches", got)
	}
	if counts["index"] < n/5 {
		t.Errorf("the index paths served %d of %d queries", counts["index"], n)
	}
}

// TestPlannerPaths serves one query of each kind and proves by the counters which path served it:
// a single-field range, a composite, a union of in values, array-contains, a merge of equality
// indexes, a collection-group override, and the reference path for what no index serves or a
// CREATING index would; each answers as the reference path does.
func TestPlannerPaths(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	for i := range 12 {
		f := value.Map(
			value.Field{Name: "a", Value: value.Int(int64(i % 3))},
			value.Field{Name: "b", Value: value.Int(int64(i % 4))},
			value.Field{Name: "tags", Value: value.Array(value.String([]string{"red", "blue"}[i%2]))},
			value.Field{Name: "m", Value: value.Map(value.Field{Name: "x", Value: value.Int(int64(i % 2))})})
		p.commit(t, setDoc(fmt.Sprintf("c/d%02d", i), f), setDoc(fmt.Sprintf("c/d%02d/g/e", i), f))
	}
	p.createIndex(t, "c", adminpb.Index_COLLECTION, ascField("a"), ascField("b"))
	p.groupOverride(t, "g", "a")
	// A composite still CREATING: never served.
	if _, err := p.env.Store.CreateIndex(ctx, p.db, "c", &adminpb.Index{QueryScope: adminpb.Index_COLLECTION,
		Fields: []*adminpb.Index_IndexField{ascField("b"), descField("a")}}); err != nil {
		t.Fatal(err)
	}
	eq := func(path string, v value.Value) *pb.StructuredQuery_Filter {
		return fieldFilter(path, pb.StructuredQuery_FieldFilter_EQUAL, v)
	}
	ref := value.Reference(docsRoot + "/c/d05")
	for _, c := range []struct {
		name string
		sq   *pb.StructuredQuery
		path query.Path
		n    int
	}{
		{"range descending", &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("b", pb.StructuredQuery_FieldFilter_GREATER_THAN, value.Int(1)),
			OrderBy: []*pb.StructuredQuery_Order{order("b", true)}, Limit: wrapperspb.Int32(4)}, query.PathIndex, 4},
		{"composite with cursor", &pb.StructuredQuery{From: from("c", false), Where: eq("a", value.Int(1)),
			OrderBy: []*pb.StructuredQuery_Order{order("b", false)}, StartAt: &pb.Cursor{Values: []*pb.Value{wire.ValueToProto(value.Int(1))}},
			Offset: 1}, query.PathIndex, 1},
		{"in", &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_IN,
			value.Array(value.Int(0), value.Int(2))), OrderBy: []*pb.StructuredQuery_Order{order("b", false)}}, query.PathIndex, 8},
		{"array-contains", &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("tags", pb.StructuredQuery_FieldFilter_ARRAY_CONTAINS,
			value.String("red")), Limit: wrapperspb.Int32(5)}, query.PathIndexContains, 5},
		{"merged equalities", &pb.StructuredQuery{From: from("c", false), Where: composite(pb.StructuredQuery_CompositeFilter_AND,
			eq("a", value.Int(1)), eq("m.x", value.Int(0)))}, query.PathIndexMerged, 2},
		{"collection group", &pb.StructuredQuery{From: from("g", true), Where: eq("a", value.Int(2))}, query.PathIndex, 4},
		{"names alone", &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("__name__", pb.StructuredQuery_FieldFilter_GREATER_THAN, ref)},
			query.PathReference, 6},
		{"a CREATING composite", &pb.StructuredQuery{From: from("c", false),
			OrderBy: []*pb.StructuredQuery_Order{order("b", false), order("a", true)}}, query.PathReference, 12},
	} {
		before, all := query.ServedCount(c.path), allServed()
		got := p.run(ctx, p.env.Service, c.sq, nil)
		if query.ServedCount(c.path) != before+1 || allServed() != all+1 {
			t.Errorf("%s: not served by %v alone", c.name, c.path)
		}
		want := p.run(ctx, p.env.Reference, c.sq, nil)
		if got.err != nil || !got.equal(want) || len(got.docs) != c.n {
			t.Errorf("%s: %v, want %d documents as the reference path's %v", c.name, got, c.n, want)
		}
	}
}

// BenchmarkIndexedQuery runs an equality and order query, limit 20, over 50,000 documents with
// its composite index: an index scan that reads about as many entries as it returns
// (entries/op), where the reference path would read the whole collection.
func BenchmarkIndexedQuery(b *testing.B) {
	env := apitest.New(b, apitest.Options{})
	db := core.DB{Project: env.Number, ProjectID: apitest.Project, ID: core.DefaultDatabase}
	ctx := b.Context()
	const docs, batch = 50_000, 500
	for i := 0; i < docs; i += batch {
		writes := make([]core.Write, batch)
		for j := range writes {
			n := i + j
			writes[j] = setDoc(fmt.Sprintf("big/d%06d", n), value.Map(
				value.Field{Name: "a", Value: value.Int(int64(n % 50))},
				value.Field{Name: "b", Value: value.Int(int64(n * 7919 % docs))}))
		}
		if _, err := env.Store.Commit(ctx, db, writes); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := env.Store.CreateIndex(ctx, db, "big", &adminpb.Index{QueryScope: adminpb.Index_COLLECTION,
		Fields: []*adminpb.Index_IndexField{ascField("a"), ascField("b")}}); err != nil {
		b.Fatal(err)
	}
	if err := env.Store.BuildIndexes(ctx, core.IndexBuildOptions{}); err != nil {
		b.Fatal(err)
	}
	p := &planEnv{env: env, db: db, call: api.Call{Authorization: "Bearer " + env.JWT(b),
		Transport: authn.Transport{Peer: netip.MustParseAddr("127.0.0.1")}}}
	sq := &pb.StructuredQuery{From: from("big", false), Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_EQUAL, value.Int(7)),
		OrderBy: []*pb.StructuredQuery_Order{order("b", false)}, Limit: wrapperspb.Int32(20)}
	b.Run("index", func(b *testing.B) {
		served, entries := query.ServedCount(query.PathIndex), query.EntriesReadCount()
		for b.Loop() {
			if got := p.run(ctx, env.Service, sq, nil); got.err != nil || len(got.docs) != 20 {
				b.Fatalf("%v", got)
			}
		}
		ops := query.ServedCount(query.PathIndex) - served
		if ops != int64(b.N) {
			b.Fatalf("%d of %d queries served by the index path", ops, b.N)
		}
		perOp := float64(query.EntriesReadCount()-entries) / float64(ops)
		b.ReportMetric(perOp, "entries/op")
		if perOp > 100 {
			b.Fatalf("%.0f entries read per query of 20 results", perOp)
		}
	})
	b.Run("reference", func(b *testing.B) {
		for b.Loop() {
			if got := p.run(ctx, env.Reference, sq, nil); got.err != nil || len(got.docs) != 20 {
				b.Fatalf("%v", got)
			}
		}
	})
}

func allServed() int64 { return indexServed() + query.ServedCount(query.PathReference) }

// A candidate the index claims and the document refutes fails the query INTERNAL and is counted,
// never dropped.
func TestRecheckMismatch(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	p.commit(t, setDoc("c/x", value.Map(value.Field{Name: "a", Value: value.Int(2)})),
		setDoc("c/y", value.Map(value.Field{Name: "a", Value: value.Int(1)})))
	// A stray entry: c/x under a == 1.
	key := core.IndexKeyPrefix(index.ScopeCollection, nil, "c")
	key = core.AppendIndexValue(key, value.Int(1), false)
	key = core.AppendIndexName(key, core.ParsePath("c/x"), false)
	if _, err := p.env.Pool.Exec(ctx, `INSERT INTO documents.index_entries (project_number, database_id, index_id, key) VALUES ($1, $2, $3, $4)`,
		p.db.Project, p.db.ID, core.SingleFieldIndexID("c", index.ScopeCollection, "a", index.Ascending), key); err != nil {
		t.Fatal(err)
	}
	before := query.MismatchCount()
	got := p.run(ctx, p.env.Service, &pb.StructuredQuery{From: from("c", false),
		Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_EQUAL, value.Int(1))}, nil)
	if we, ok := errors.AsType[*wire.Error](got.err); !ok || we.Code != wire.CodeInternal {
		t.Errorf("a stray entry: %v", got)
	}
	if query.MismatchCount() != before+1 {
		t.Errorf("the mismatch was not counted")
	}
}
