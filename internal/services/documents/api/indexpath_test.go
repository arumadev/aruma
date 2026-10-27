package api_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The index paths' bounds (indexpath.go): a query holds no snapshot while it sends, continues at
// its read time in later snapshots, and holds a bounded number of bytes of documents.

// runAt runs a query or an aggregation at readTime (the latest when zero) and returns its answer
// and the read time it answered at.
func (p *planEnv) runAt(ctx context.Context, svc *api.Service, sq *pb.StructuredQuery,
	aggs []*pb.StructuredAggregationQuery_Aggregation, readTime time.Time) (answer, time.Time) {
	var out answer
	var rt time.Time
	if aggs != nil {
		req := &pb.RunAggregationQueryRequest{Parent: docsRoot, QueryType: &pb.RunAggregationQueryRequest_StructuredAggregationQuery{
			StructuredAggregationQuery: &pb.StructuredAggregationQuery{
				QueryType:    &pb.StructuredAggregationQuery_StructuredQuery{StructuredQuery: sq},
				Aggregations: aggs}}}
		if !readTime.IsZero() {
			req.ConsistencySelector = &pb.RunAggregationQueryRequest_ReadTime{ReadTime: timestamppb.New(readTime)}
		}
		out.err = svc.RunAggregationQuery(ctx, p.call, req, func(r *pb.RunAggregationQueryResponse) error {
			if r.GetResult() != nil {
				out.agg, rt = r.GetResult(), r.GetReadTime().AsTime()
			}
			return nil
		})
		return out, rt
	}
	req := &pb.RunQueryRequest{Parent: docsRoot, QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: sq}}
	if !readTime.IsZero() {
		req.ConsistencySelector = &pb.RunQueryRequest_ReadTime{ReadTime: timestamppb.New(readTime)}
	}
	first := true
	out.err = svc.RunQuery(ctx, p.call, req, func(r *pb.RunQueryResponse) error {
		if first {
			out.skipped, first = r.GetSkippedResults(), false
		}
		rt = r.GetReadTime().AsTime()
		if r.GetDocument() != nil {
			out.docs = append(out.docs, r.GetDocument())
		}
		return nil
	})
	return out, rt
}

// TestPlannerDifferentialResumed is TestPlannerDifferential with every index path query released
// and continued in a new snapshot after each batch of one to three results, and commits
// (updates, deletes and creates in the collections queried) between the batches: each answer
// must be the reference path's at the read time the planner answered at.
func TestPlannerDifferentialResumed(t *testing.T) {
	p, g := differentialSetup(t)
	ctx := t.Context()
	var resumes atomic.Int64
	after := func() {
		resumes.Add(1)
		// A few documents of the 230 or so, so that some a query has yet to reach change.
		writes := map[string]core.Write{}
		for range 5 + g.r.IntN(16) {
			path := g.names[g.r.IntN(len(g.names))]
			switch g.r.IntN(4) {
			case 0:
				writes[path] = core.Write{Kind: core.WriteDelete, Path: core.ParsePath(path)}
			case 1:
				path = g.path()
				g.names = append(g.names, path)
				writes[path] = setDoc(path, g.fields())
			default:
				writes[path] = setDoc(path, g.fields())
			}
		}
		var ws []core.Write
		for _, path := range slices.Sorted(maps.Keys(writes)) {
			ws = append(ws, writes[path])
		}
		if _, err := p.env.Store.Commit(ctx, p.db, ws); err != nil {
			t.Errorf("a commit between batches: %v", err)
		}
	}
	mismatches := query.MismatchCount()
	n := 600
	if testing.Short() {
		n = 150
	}
	counts := map[string]int{}
	for i := range n {
		api.SetIndexBatch(p.env.Service, 1+g.r.IntN(3), after)
		sq := g.query()
		var aggs []*pb.StructuredAggregationQuery_Aggregation
		if i%7 == 0 {
			aggs = []*pb.StructuredAggregationQuery_Aggregation{
				{Alias: "n", Operator: &pb.StructuredAggregationQuery_Aggregation_Count_{Count: &pb.StructuredAggregationQuery_Aggregation_Count{}}},
			}
		}
		before, resumed := indexServed(), resumes.Load()
		got, rt := p.runAt(ctx, p.env.Service, sq, aggs, time.Time{})
		served := indexServed() > before
		api.SetIndexBatch(p.env.Service, 0, nil)
		if query.MismatchCount() != mismatches {
			t.Fatalf("query %d %v: a recheck mismatch: %v", i, sq, got)
		}
		if !served && got.err == nil {
			continue // the reference path served it, at the latest commit
		}
		// An error is compared with the reference path's at the latest commit (rt is zero).
		want, _ := p.runAt(ctx, p.env.Reference, sq, aggs, rt)
		switch {
		case !got.equal(want):
			t.Errorf("query %d %v at %v:\nplanner   %v\nreference %v", i, sq, rt, got, want)
		case got.err != nil:
			counts["error"]++
		case resumes.Load() > resumed:
			counts["resumed"]++
		default:
			counts["one batch"]++
		}
	}
	t.Logf("%d queries: %v; %d commits between batches", n, counts, resumes.Load())
	if counts["resumed"] < n/10 {
		t.Errorf("only %d of %d queries continued in a later snapshot", counts["resumed"], n)
	}
}

// A client that stops reading holds no pool connection and no snapshot: with more stalled
// queries than the pool has connections, a commit still goes through, and nothing is acquired
// while they wait.
func TestIndexPathStalledClient(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	writes := make([]core.Write, 300)
	for i := range writes {
		writes[i] = setDoc(fmt.Sprintf("c/d%03d", i), value.Map(value.Field{Name: "a", Value: value.Int(1)}))
	}
	p.commit(t, writes...)
	sq := &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_EQUAL, value.Int(1))}

	stalled := int(p.env.Pool.Config().MaxConns) + 2
	release := make(chan struct{})
	var waiting, wg sync.WaitGroup
	waiting.Add(stalled)
	before := indexServed()
	for range stalled {
		wg.Go(func() {
			sent := 0
			req := &pb.RunQueryRequest{Parent: docsRoot, QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: sq}}
			err := p.env.Service.RunQuery(ctx, p.call, req, func(*pb.RunQueryResponse) error {
				if sent++; sent == 2 {
					waiting.Done()
					<-release // a client that stops reading
				}
				return nil
			})
			if err != nil || sent != 300 {
				t.Errorf("a stalled query: %v after %d results", err, sent)
			}
		})
	}
	waiting.Wait()
	if n := p.env.Pool.Stat().AcquiredConns(); n != 0 {
		t.Errorf("%d connections acquired while %d clients stall", n, stalled)
	}
	var idle int
	if err := p.env.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND state LIKE 'idle in transaction%'`).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	if idle != 0 {
		t.Errorf("%d sessions idle in a transaction while clients stall", idle)
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := p.env.Store.Commit(cctx, p.db, []core.Write{setDoc("c/x", value.Map())}); err != nil {
		t.Errorf("a commit while %d clients stall: %v", stalled, err)
	}
	cancel()
	close(release)
	wg.Wait()
	if got := indexServed() - before; got != int64(stalled) {
		t.Errorf("the index path served %d of %d queries", got, stalled)
	}
}

// A union of 30 streams over large documents holds a bounded number of bytes of them at once,
// not every stream's page of documents, and answers as the reference path does.
func TestIndexPathUnionMemory(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	const values, perValue, size = 30, 6, 400 << 10
	big := value.String(strings.Repeat("x", size))
	vals := make([]value.Value, values)
	for v := range values {
		vals[v] = value.Int(int64(v))
		var writes []core.Write
		for i := range perValue {
			writes = append(writes, setDoc(fmt.Sprintf("c/v%02d-%d", v, i), value.Map(
				value.Field{Name: "a", Value: value.Int(int64(v))}, value.Field{Name: "s", Value: big})))
		}
		p.commit(t, writes...)
	}
	var peak int
	api.ObserveIndexPeak(p.env.Service, func(n int) { peak = n })
	sq := &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_IN, value.Array(vals...))}
	before := indexServed()
	got := p.run(ctx, p.env.Service, sq, nil)
	if indexServed() != before+1 {
		t.Fatal("the in was not served by an index path")
	}
	if want := p.run(ctx, p.env.Reference, sq, nil); got.err != nil || len(got.docs) != values*perValue || !got.equal(want) {
		t.Fatalf("%v, want the reference path's %v", got, want)
	}
	// One document per stream fetched at a time, each stream's head, and a batch: well below
	// the 72 MiB of all of them.
	const bound = 32 << 20
	if peak == 0 || peak > bound {
		t.Errorf("held %d bytes of documents at once, want at most %d", peak, bound)
	}
	t.Logf("held at most %d bytes of %d", peak, values*perValue*size)
}

// A query continued in a later snapshot whose catalogue changed goes on when its plan reads the
// same indexes (another index was created), and fails ABORTED when its index was deleted.
func TestIndexPathCatalogChange(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	writes := make([]core.Write, 10)
	for i := range writes {
		writes[i] = setDoc(fmt.Sprintf("c/d%02d", i), value.Map(value.Field{Name: "a", Value: value.Int(1)},
			value.Field{Name: "b", Value: value.Int(int64(9 - i))}))
	}
	p.commit(t, writes...)
	p.createIndex(t, "c", adminpb.Index_COLLECTION, ascField("a"), ascField("b"))
	sq := &pb.StructuredQuery{From: from("c", false), Where: fieldFilter("a", pb.StructuredQuery_FieldFilter_EQUAL, value.Int(1)),
		OrderBy: []*pb.StructuredQuery_Order{order("b", false)}}

	once := func(f func()) func() {
		done := false
		return func() {
			if !done {
				done = true
				f()
			}
		}
	}
	api.SetIndexBatch(p.env.Service, 3, once(func() {
		if _, err := p.env.Store.CreateIndex(ctx, p.db, "c", &adminpb.Index{QueryScope: adminpb.Index_COLLECTION,
			Fields: []*adminpb.Index_IndexField{ascField("b"), descField("a")}}); err != nil {
			t.Error(err)
		}
	}))
	before := indexServed()
	got := p.run(ctx, p.env.Service, sq, nil)
	if want := p.run(ctx, p.env.Reference, sq, nil); indexServed() != before+1 || got.err != nil || len(got.docs) != 10 || !got.equal(want) {
		t.Errorf("another index created: %v, want the reference path's %v", got, want)
	}

	api.SetIndexBatch(p.env.Service, 3, once(func() {
		ixs, err := p.env.Store.ListIndexes(ctx, p.db, "c")
		if err != nil {
			t.Error(err)
		}
		for _, ix := range ixs {
			if len(ix.GetFields()) > 0 && ix.GetFields()[0].GetFieldPath() == "a" {
				if err := p.env.Store.DeleteIndex(ctx, p.db, "c", ix.GetName()[strings.LastIndex(ix.GetName(), "/")+1:]); err != nil {
					t.Error(err)
				}
			}
		}
	}))
	got = p.run(ctx, p.env.Service, sq, nil)
	if we, ok := errors.AsType[*wire.Error](got.err); !ok || we.Code != wire.CodeAborted || len(got.docs) != 3 {
		t.Errorf("its index deleted: %v", got)
	}
	api.SetIndexBatch(p.env.Service, 0, nil)
}

// An equality or an in on __name__ bounds the scan to that name's entries: a == 1 AND __name__
// in three names reads about three entries, not three times every entry of a == 1.
func TestNameEqualityBoundsScan(t *testing.T) {
	p := newPlanEnv(t)
	ctx := t.Context()
	writes := make([]core.Write, 300)
	for i := range writes {
		writes[i] = setDoc(fmt.Sprintf("c/d%03d", i), value.Map(value.Field{Name: "a", Value: value.Int(1)}))
	}
	p.commit(t, writes...)
	ref := func(id string) value.Value { return value.Reference(docsRoot + "/c/" + id) }
	sq := &pb.StructuredQuery{From: from("c", false), Where: composite(pb.StructuredQuery_CompositeFilter_AND,
		fieldFilter("a", pb.StructuredQuery_FieldFilter_EQUAL, value.Int(1)),
		fieldFilter("__name__", pb.StructuredQuery_FieldFilter_IN, value.Array(ref("d007"), ref("d150"), ref("x"))))}
	before, entries := indexServed(), query.EntriesReadCount()
	got := p.run(ctx, p.env.Service, sq, nil)
	if indexServed() != before+1 {
		t.Fatal("not served by an index path")
	}
	if n := query.EntriesReadCount() - entries; n > 10 {
		t.Errorf("%d entries read for two results", n)
	}
	if want := p.run(ctx, p.env.Reference, sq, nil); got.err != nil || len(got.docs) != 2 || !got.equal(want) {
		t.Errorf("%v, want the reference path's %v", got, want)
	}
}
