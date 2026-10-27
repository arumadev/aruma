package grpc_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// TestIndexesReplay sends the 105 recorded shapes of indexes-105.json, recorded over REST,
// through RunQuery and RunAggregationQuery: FAILED_PRECONDITION with the same message, or the
// same documents in order, or the same aggregates.
func TestIndexesReplay(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok, c := t.Context(), x.env.JWT(t), x.client
	for _, d := range apitest.IndexSeed() {
		doc := &pb.Document{}
		if err := protojson.Unmarshal([]byte(`{"fields":`+d.Fields+`}`), doc); err != nil {
			t.Fatal(err)
		}
		doc.Name = root + "/" + d.Path
		if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: doc}, tok)); err != nil {
			t.Fatalf("seed %s: %v", d.Path, err)
		}
	}
	// run sends a case on the never-written collection or the populated one.
	run := func(ic *apitest.IndexCase, empty bool) (ids []string, aggs []json.RawMessage, err error) {
		parent, method, _ := strings.Cut(strings.ReplaceAll(ic.Request.Path, "{project}", apitest.Project), ":")
		body := []byte(ic.Body(root, empty))
		if method == "runQuery" {
			r := &pb.RunQueryRequest{}
			if err := protojson.Unmarshal(body, r); err != nil {
				t.Fatal(err)
			}
			r.Parent = parent
			s, err := c.RunQuery(ctx, req(r, tok))
			if err != nil {
				return nil, nil, err
			}
			for s.Receive() {
				if d := s.Msg().Document; d != nil {
					ids = append(ids, apitest.LastSegments(d.Name))
				}
			}
			return ids, nil, s.Err()
		}
		r := &pb.RunAggregationQueryRequest{}
		if err := protojson.Unmarshal(body, r); err != nil {
			t.Fatal(err)
		}
		r.Parent = parent
		s, err := c.RunAggregationQuery(ctx, req(r, tok))
		if err != nil {
			return nil, nil, err
		}
		for s.Receive() {
			if res := s.Msg().Result; res != nil {
				b, merr := wire.MarshalREST(res)
				if merr != nil {
					t.Fatal(merr)
				}
				var f struct {
					AggregateFields json.RawMessage `json:"aggregateFields"`
				}
				if err := json.Unmarshal(b, &f); err != nil {
					t.Fatal(err)
				}
				aggs = append(aggs, f.AggregateFields)
			}
		}
		return nil, aggs, s.Err()
	}
	matched := 0
	cases := apitest.IndexCases(t)
	indexed := indexServed()
	for i := range cases {
		ic := &cases[i]
		for _, empty := range []bool{true, false} {
			want := ic.Want(empty)
			ids, aggs, err := run(ic, empty)
			ok := false
			switch ce, isStatus := errors.AsType[*connect.Error](err); {
			case want.Verdict != "ok":
				ok = isStatus && ce.Code() == connect.CodeFailedPrecondition && want.Verdict == "FAILED_PRECONDITION" && ce.Message() == want.Message
			case err != nil:
			case want.Aggregates != nil:
				ok = len(aggs) == len(want.Aggregates)
				for i := 0; ok && i < len(aggs); i++ {
					var g, w any
					ok = json.Unmarshal(aggs[i], &g) == nil && json.Unmarshal(want.Aggregates[i], &w) == nil && reflect.DeepEqual(g, w)
				}
			default:
				ok = slices.Equal(ids, want.IDs) || len(ids) == 0 && len(want.IDs) == 0
			}
			if !ok {
				t.Errorf("%s (empty %v, %s): got %v ids %v aggregates %s, want %+v", ic.ID, empty, ic.Description, err, ids, aggs, want)
				continue
			}
			matched++
		}
	}
	if matched != 210 {
		t.Errorf("%d of 210 answers as production's", matched)
	}
	// The automatic single-field indexes serve some of the shapes production serves.
	if indexServed() == indexed {
		t.Error("no recorded shape was served by an index path")
	}
	// With the indexes production asked for, each needs-index shape is served by one.
	n := apitest.IndexedReplay(t, x.env, root, cases, func(ic *apitest.IndexCase) (apitest.IndexAnswer, error) {
		ids, aggs, err := run(ic, false)
		return apitest.IndexAnswer{IDs: ids, Aggregates: aggs}, err
	})
	t.Logf("%d needs-index shapes replayed with their indexes", n)
}

func indexServed() int64 {
	return query.ServedCount(query.PathIndex) + query.ServedCount(query.PathIndexMerged) + query.ServedCount(query.PathIndexContains)
}
