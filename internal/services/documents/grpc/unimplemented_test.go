package grpc_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// runQueryErr runs a RunQuery to its end and returns its error, from the call or the stream.
func runQueryErr(t *testing.T, x *harness, tok string, q *pb.StructuredQuery, explain *pb.ExplainOptions) error {
	s, err := x.client.RunQuery(t.Context(), req(&pb.RunQueryRequest{Parent: root, ExplainOptions: explain,
		QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: q}}, tok))
	if err != nil {
		return err
	}
	for s.Receive() {
	}
	return s.Err()
}

// TestExplainOptionsRejected: explain_options on RunQuery and RunAggregationQuery answer
// UNIMPLEMENTED naming Query Explain, for analyze false, {} and true alike, after index
// enforcement: a query that needs an index gets production's needs-index error first (parity
// exception explain-rejected, f0-explain.json).
func TestExplainOptionsRejected(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	plain := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "aruma-ref-f0-explain"}}}
	const msg = "Query Explain is not supported by this server"
	for name, o := range map[string]*pb.ExplainOptions{
		"empty": {}, "analyze false": {Analyze: false}, "analyze true": {Analyze: true},
	} {
		wantErr(t, "RunQuery "+name, runQueryErr(t, x, tok, plain, o), connect.CodeUnimplemented, msg)

		s, err := x.client.RunAggregationQuery(t.Context(), req(&pb.RunAggregationQueryRequest{Parent: root, ExplainOptions: o,
			QueryType: &pb.RunAggregationQueryRequest_StructuredAggregationQuery{StructuredAggregationQuery: &pb.StructuredAggregationQuery{
				QueryType:    &pb.StructuredAggregationQuery_StructuredQuery{StructuredQuery: plain},
				Aggregations: []*pb.StructuredAggregationQuery_Aggregation{{Alias: "n", Operator: &pb.StructuredAggregationQuery_Aggregation_Count_{Count: &pb.StructuredAggregationQuery_Aggregation_Count{}}}},
			}}}, tok))
		if err == nil {
			for s.Receive() {
			}
			err = s.Err()
		}
		wantErr(t, "RunAggregationQuery "+name, err, connect.CodeUnimplemented, msg)
	}
	// Without explain_options the same query runs.
	if err := runQueryErr(t, x, tok, plain, nil); err != nil {
		t.Errorf("RunQuery without explain_options: %v", err)
	}

	// A query that needs a composite index gets the needs-index error, not the explain refusal.
	needs := &pb.StructuredQuery{
		From:    []*pb.StructuredQuery_CollectionSelector{{CollectionId: "aruma-ref-f0-explain"}},
		Where:   fieldEquals("a", 1),
		OrderBy: []*pb.StructuredQuery_Order{{Field: &pb.StructuredQuery_FieldReference{FieldPath: "b"}}},
	}
	err := runQueryErr(t, x, tok, needs, &pb.ExplainOptions{})
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok || ce.Code() != connect.CodeFailedPrecondition || !strings.HasPrefix(ce.Message(), "The query requires an index.") {
		t.Errorf("explain on a query that needs an index: %v, want FAILED_PRECONDITION needs-index", err)
	}
}

func fieldEquals(path string, n int64) *pb.StructuredQuery_Filter {
	return &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
		Field: &pb.StructuredQuery_FieldReference{FieldPath: path}, Op: pb.StructuredQuery_FieldFilter_EQUAL,
		Value: &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}}}}}
}

// TestFindNearestWithVectorIndexRejected: find_nearest with no matching vector index is
// production's FAILED_PRECONDITION with its gcloud command; once a matching READY vector index
// exists (created through the Admin API) the same query is UNIMPLEMENTED naming findNearest; an
// index of another dimension, field or prefix does not match (parity exception
// find-nearest-with-vector-index, f0-find-nearest.json).
func TestFindNearestWithVectorIndexRejected(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	admin, _ := adminClients(x)
	tok := x.env.JWT(t)

	vec := &pb.Value{ValueType: &pb.Value_MapValue{MapValue: &pb.MapValue{Fields: map[string]*pb.Value{
		"__type__": {ValueType: &pb.Value_StringValue{StringValue: "__vector__"}},
		"value": {ValueType: &pb.Value_ArrayValue{ArrayValue: &pb.ArrayValue{Values: []*pb.Value{
			{ValueType: &pb.Value_DoubleValue{DoubleValue: 1}}, {ValueType: &pb.Value_DoubleValue{DoubleValue: 2}},
			{ValueType: &pb.Value_DoubleValue{DoubleValue: 3}}}}}},
	}}}}
	query := func(prefilter bool) *pb.StructuredQuery {
		q := &pb.StructuredQuery{
			From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "vec"}},
			FindNearest: &pb.StructuredQuery_FindNearest{
				VectorField:     &pb.StructuredQuery_FieldReference{FieldPath: "embedding"},
				QueryVector:     vec,
				DistanceMeasure: pb.StructuredQuery_FindNearest_EUCLIDEAN,
				Limit:           wrapperspb.Int32(2),
			},
		}
		if prefilter {
			q.Where = fieldEquals("kind", 1)
		}
		return q
	}
	const missing = "Missing vector index configuration. Please create the required index with the following gcloud command: " +
		"gcloud firestore indexes composite create --project=" + apitest.Project + " --collection-group=vec --query-scope=COLLECTION"
	const flat = ` --field-config=vector-config='{"dimension":"3","flat": "{}"}',field-path=embedding`
	wantErr(t, "no vector index", runQueryErr(t, x, tok, query(false), nil), connect.CodeFailedPrecondition, missing+flat)
	wantErr(t, "no vector index, prefilter", runQueryErr(t, x, tok, query(true), nil), connect.CodeFailedPrecondition,
		missing+" --field-config=order=ASCENDING,field-path=kind"+flat)

	create := func(dim int32, prefix ...string) {
		t.Helper()
		var fs []*adminpb.Index_IndexField
		for _, p := range prefix {
			fs = append(fs, &adminpb.Index_IndexField{FieldPath: p, ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}})
		}
		fs = append(fs, &adminpb.Index_IndexField{FieldPath: "embedding", ValueMode: &adminpb.Index_IndexField_VectorConfig_{
			VectorConfig: &adminpb.Index_IndexField_VectorConfig{Dimension: dim, Type: &adminpb.Index_IndexField_VectorConfig_Flat{Flat: &adminpb.Index_IndexField_VectorConfig_FlatIndex{}}}}})
		if _, err := admin.CreateIndex(t.Context(), req(&adminpb.CreateIndexRequest{Parent: db + "/collectionGroups/vec",
			Index: &adminpb.Index{QueryScope: adminpb.Index_COLLECTION, Fields: fs}}, tok)); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
	}
	const served = "findNearest (vector search) is not supported by this server"

	// A vector index of another dimension, and one with a prefix the query lacks, do not match.
	create(4)
	create(3, "kind")
	wantErr(t, "other dimension", runQueryErr(t, x, tok, query(false), nil), connect.CodeFailedPrecondition, missing+flat)
	wantErr(t, "prefix not in the query", runQueryErr(t, x, tok, query(false), nil), connect.CodeFailedPrecondition, missing+flat)
	wantErr(t, "prefilter matches the prefixed index", runQueryErr(t, x, tok, query(true), nil), connect.CodeUnimplemented, served)

	create(3)
	wantErr(t, "matching vector index", runQueryErr(t, x, tok, query(false), nil), connect.CodeUnimplemented, served)
}

// TestListenForeignDatabaseTarget: a Listen target naming another project or another database of
// the project, alone or mixed with the stream's own documents, is removed with production's
// INVALID_ARGUMENT "Cannot add a target for a different database." (f4-listen.json), and the
// stream stays open for the next target (parity exception mixed-project-resource-names).
func TestListenForeignDatabaseTarget(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("lf/a", rankDoc(1)))
	l := apitest.OpenListen(t, x.client, tok)
	const want = `"Cannot add a target for a different database."`
	for id, foreign := range map[int32]string{
		3: other + "/documents/lf/a",
		4: "projects/" + apitest.Project + "/databases/second/documents/lf/a",
	} {
		l.Add(&pb.Target{TargetId: id, TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{
			Documents: []string{root + "/lf/a", foreign}}}})
		wantShapes(t, "foreign target "+foreign, l.Until(func(r *pb.ListenResponse) bool { return r.GetTargetChange() != nil }),
			fmt.Sprintf("REMOVE[%d] 3 %s", id, want))
	}
	l.Add(docTarget(5, "lf/a"))
	wantShapes(t, "next target", l.UntilGlobal(), "ADD[5]", "change lf/a [5]", "CURRENT[5]", "global")
}
