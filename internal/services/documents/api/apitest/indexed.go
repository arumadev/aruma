package apitest

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/api"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The replays of indexes-105.json with the indexes production asked for (ADR-025 §1): each
// needs-index shape's indexes are created through core and built, and the shape is then served
// by an index path, as the reference path answers it.

// IndexAnswer is what a transport answered a case: the documents' last three name segments in
// order, or the aggregates as REST writes them.
type IndexAnswer struct {
	IDs        []string
	Aggregates []json.RawMessage
}

// IndexedReplay creates the indexes the needs-index cases of cases asked for on the populated
// collection, builds them, then sends each such case through run, the transport, on the
// populated collection under root: it must succeed, be served by an index path when its filter
// and order need the index (an aggregation that needs one for its fields alone stays on the
// reference path), and answer as the reference path does. It returns how many cases it replayed.
func IndexedReplay(t *testing.T, e *Env, root string, cases []IndexCase, run func(c *IndexCase) (IndexAnswer, error)) int {
	t.Helper()
	ctx := t.Context()
	db := documents.DB{Project: e.Number, ProjectID: Project, ID: documents.DefaultDatabase}
	type replay struct {
		c *IndexCase
		// indexed: the query's filter and order need the index, not only its aggregations,
		// whose fields an index path cannot hold for documents without them.
		indexed bool
	}
	var needed []replay
	for i := range cases {
		c := &cases[i]
		if c.Verdict != "FAILED_PRECONDITION" {
			continue
		}
		q, aggs, parent := c.parse(t, root)
		cat, err := e.Store.IndexCatalog(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		pq, err := query.Parse(q, parent)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		var shapes []index.Aggregation
		if aggs != nil {
			as, err := query.AggregationsOf(aggs)
			if err != nil {
				t.Fatal(err)
			}
			shapes = query.Shapes(as)
		}
		missing := index.Check(pq.Shape(shapes), cat, index.Enforce).Missing
		if slices.ContainsFunc(missing, func(r index.Required) bool {
			return !r.Exemption && len(r.Index.Fields) == 1 && r.Index.Fields[0].Path == index.NameField
		}) {
			// __name__ descending alone, which no index the Admin API creates holds.
			continue
		}
		for _, r := range missing {
			createRequired(t, e, db, r)
		}
		needed = append(needed, replay{c, aggs == nil || index.Check(pq.Shape(nil), nil, index.Enforce).Verdict == index.NeedsIndex})
	}
	if err := e.Store.BuildIndexes(ctx, documents.IndexBuildOptions{}); err != nil {
		t.Fatal(err)
	}
	call := api.Call{Authorization: "Bearer " + e.JWT(t), Transport: authn.Transport{Peer: netip.MustParseAddr("127.0.0.1")}}
	for _, r := range needed {
		c := r.c
		before := indexServed()
		got, err := run(c)
		if err != nil {
			t.Errorf("%s (%s) with its indexes: %v", c.ID, c.Description, err)
			continue
		}
		if r.indexed && indexServed() != before+1 {
			t.Errorf("%s (%s) with its indexes: not served by an index path", c.ID, c.Description)
		}
		want := c.reference(t, e, call, root)
		if !slices.Equal(got.IDs, want.IDs) || !sameAggregates(got.Aggregates, want.Aggregates) {
			t.Errorf("%s (%s) with its indexes: %v %s, the reference path %v %s", c.ID, c.Description, got.IDs, got.Aggregates,
				want.IDs, want.Aggregates)
		}
	}
	return len(needed)
}

func indexServed() int64 {
	return query.ServedCount(query.PathIndex) + query.ServedCount(query.PathIndexMerged) + query.ServedCount(query.PathIndexContains)
}

func sameAggregates(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		var x, y any
		if json.Unmarshal(a[i], &x) != nil || json.Unmarshal(b[i], &y) != nil || !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// parse reads the case's query on the populated collection: its StructuredQuery, its
// aggregations (nil for a plain query) and its parent.
func (c *IndexCase) parse(t *testing.T, root string) (*pb.StructuredQuery, []*pb.StructuredAggregationQuery_Aggregation, wire.Name) {
	t.Helper()
	parentName, method, _ := strings.Cut(strings.ReplaceAll(c.Request.Path, "{project}", Project), ":")
	parent, err := wire.ParseParentName(parentName)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(c.Body(root, false))
	if method == "runQuery" {
		r := &pb.RunQueryRequest{}
		if err := protojson.Unmarshal(body, r); err != nil {
			t.Fatal(err)
		}
		return r.GetStructuredQuery(), nil, parent
	}
	r := &pb.RunAggregationQueryRequest{}
	if err := protojson.Unmarshal(body, r); err != nil {
		t.Fatal(err)
	}
	saq := r.GetStructuredAggregationQuery()
	return saq.GetStructuredQuery(), saq.GetAggregations(), parent
}

// reference is the case's answer on the reference path alone.
func (c *IndexCase) reference(t *testing.T, e *Env, call api.Call, root string) IndexAnswer {
	t.Helper()
	q, aggs, _ := c.parse(t, root)
	parent, _, _ := strings.Cut(strings.ReplaceAll(c.Request.Path, "{project}", Project), ":")
	var out IndexAnswer
	var err error
	if aggs == nil {
		req := &pb.RunQueryRequest{Parent: parent, QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: q}}
		err = e.Reference.RunQuery(t.Context(), call, req, func(r *pb.RunQueryResponse) error {
			if d := r.GetDocument(); d != nil {
				out.IDs = append(out.IDs, LastSegments(d.GetName()))
			}
			return nil
		})
	} else {
		req := &pb.RunAggregationQueryRequest{Parent: parent, QueryType: &pb.RunAggregationQueryRequest_StructuredAggregationQuery{
			StructuredAggregationQuery: &pb.StructuredAggregationQuery{
				QueryType: &pb.StructuredAggregationQuery_StructuredQuery{StructuredQuery: q}, Aggregations: aggs}}}
		err = e.Reference.RunAggregationQuery(t.Context(), call, req, func(r *pb.RunAggregationQueryResponse) error {
			if res := r.GetResult(); res != nil {
				b, err := wire.MarshalREST(res)
				if err != nil {
					return err
				}
				var f struct {
					AggregateFields json.RawMessage `json:"aggregateFields"`
				}
				if err := json.Unmarshal(b, &f); err != nil {
					return err
				}
				out.Aggregates = append(out.Aggregates, f.AggregateFields)
			}
			return nil
		})
	}
	if err != nil {
		t.Fatalf("%s on the reference path: %v", c.ID, err)
	}
	return out
}

// createRequired creates one index production asked for: a composite, or a collection-group
// single-field index by a field override that keeps the field's automatic indexes.
func createRequired(t *testing.T, e *Env, db documents.DB, r index.Required) {
	t.Helper()
	field := func(f index.IndexField) *adminpb.Index_IndexField {
		out := &adminpb.Index_IndexField{FieldPath: f.Path}
		switch f.Mode {
		case index.Contains:
			out.ValueMode = &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}
		case index.Descending:
			out.ValueMode = &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_DESCENDING}
		default:
			out.ValueMode = &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}
		}
		return out
	}
	ix := r.Index
	if r.Exemption {
		f := ix.Fields[0]
		one := func(scope adminpb.Index_QueryScope, mode index.FieldMode) *adminpb.Index {
			return &adminpb.Index{QueryScope: scope, Fields: []*adminpb.Index_IndexField{field(index.IndexField{Path: f.Path, Mode: mode})}}
		}
		// Every mode at both scopes, so a later case on the field finds its own.
		cfg := &adminpb.Field_IndexConfig{}
		for _, m := range []index.FieldMode{index.Ascending, index.Descending, index.Contains} {
			cfg.Indexes = append(cfg.Indexes, one(adminpb.Index_COLLECTION, m), one(adminpb.Index_COLLECTION_GROUP, m))
		}
		if _, err := e.Store.UpdateField(t.Context(), db, ix.CollectionGroup, &adminpb.Field{Name: db.FieldName(ix.CollectionGroup, f.Path),
			IndexConfig: cfg}, []string{"indexConfig"}); err != nil {
			t.Fatal(err)
		}
		// One field update at a time: build it before the next.
		if err := e.Store.BuildIndexes(t.Context(), documents.IndexBuildOptions{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	spec := &adminpb.Index{QueryScope: adminpb.Index_QueryScope(ix.Scope)}
	for _, f := range ix.Fields {
		spec.Fields = append(spec.Fields, field(f))
	}
	if _, err := e.Store.CreateIndex(t.Context(), db, ix.CollectionGroup, spec); err != nil {
		t.Fatalf("index %v: %v", ix, err)
	}
	// A CREATING index serves nothing (R21), so a later case that needs it would ask for it again.
	if err := e.Store.BuildIndexes(t.Context(), documents.IndexBuildOptions{}); err != nil {
		t.Fatal(err)
	}
}
