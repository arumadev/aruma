package api

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The F3 methods: RunQuery, RunAggregationQuery and PartitionQuery (ADR-025 §1, §4 to §6).
//
// A query is checked as production checks it (query.Parse), then against the database: it must
// exist; a find_nearest is answered by the vector indexes; the query's shape meets production's
// composite-index rule under the database's enforcement (index.Check), and a query that needs an
// index fails with production's error and console link; explain_options, which production
// serves and this server does not yet, is refused after that check (f0-explain.json
// analyze_true_missing_index). Then an index path serves it when the planner serves it exactly
// (query.Plan; indexpath.go): index entries and their documents read in one snapshot, each
// candidate checked again by the reference matcher. Otherwise the reference path runs it: core
// reads the candidates page by page in name order (core.Queries.Scan), at one read time, in the
// request's transaction or the one it begins, and package query filters, orders and bounds them.

// errAggregationDone stops an aggregation's read once nothing later can change its result.
var errAggregationDone = errors.New("aggregation complete")

// scanPage is how many candidates the reference path reads at a time.
const scanPage = 300

// aggregationTimeout is production's bound on an aggregation: "If an aggregation can't resolve
// within 60 seconds, it returns a DEADLINE_EXCEEDED error" (OF §2b).
const aggregationTimeout = 60 * time.Second

// RunQuery runs a query and sends its results through send: one response per document, each
// with the read time; when the offset skipped results, a response with the read time and
// skipped_results alone before them; none sent is one response with the read time, and the
// skipped results if any (f0-errors.json rest.not_in_10; f3-run-query.json offset.*,
// limit.0_offset). With new_transaction the
// transaction's id is the first response, alone (f2-transactions.json
// read.run_query_new_transaction_*).
func (s *Service) RunQuery(ctx context.Context, c Call, req *pb.RunQueryRequest, send func(*pb.RunQueryResponse) error) (err error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return err
	}
	q, err := query.Parse(req.GetStructuredQuery(), parent)
	if err != nil {
		return err
	}
	var txn *core.TransactionRef
	if t, ok := req.GetConsistencySelector().(*pb.RunQueryRequest_Transaction); ok {
		txn = a.transaction(t.Transaction)
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return err
	}
	if err := s.checkQuery(ctx, a, q, nil, req.GetExplainOptions() != nil); err != nil {
		return err
	}
	if nt, ok := req.GetConsistencySelector().(*pb.RunQueryRequest_NewTransaction); ok {
		id, end, err := s.newTransaction(ctx, a, nt.NewTransaction)
		if err != nil {
			return err
		}
		defer func() { end(err) }()
		ro.Transaction = a.transaction(id)
		var flush func() error
		send, flush = transactionFirst(send, &pb.RunQueryResponse{Transaction: id})
		defer func() {
			if err == nil {
				err = flush()
			}
		}()
	}
	sent := false
	skipped, readTime, err := s.runQuery(ctx, a, parent, q, ro, false, func(d *query.Doc, rt time.Time, skipped int) error {
		if !sent && skipped > 0 {
			if err := send(&pb.RunQueryResponse{ReadTime: timestamppb.New(rt), SkippedResults: int32(skipped)}); err != nil {
				return err
			}
		}
		sent = true
		return send(&pb.RunQueryResponse{Document: queryDocument(d), ReadTime: timestamppb.New(rt)})
	})
	if err != nil {
		return err
	}
	if !sent {
		return send(&pb.RunQueryResponse{ReadTime: timestamppb.New(readTime), SkippedResults: int32(skipped)})
	}
	return nil
}

// RunAggregationQuery runs a query's aggregations and sends their one result, with the read time
// (indexes-105.json, the aggregations; f2-transactions.json read.aggregation_*). With
// new_transaction the transaction's id is the first response, alone.
func (s *Service) RunAggregationQuery(ctx context.Context, c Call, req *pb.RunAggregationQueryRequest,
	send func(*pb.RunAggregationQueryResponse) error) (err error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return err
	}
	saq := req.GetStructuredAggregationQuery()
	sq, err := query.AggregationQuery(saq)
	if err != nil {
		return err
	}
	q, err := query.Parse(sq, parent)
	if err != nil {
		return err
	}
	aggs, err := query.AggregationsOf(saq.GetAggregations())
	if err != nil {
		return err
	}
	var txn *core.TransactionRef
	if t, ok := req.GetConsistencySelector().(*pb.RunAggregationQueryRequest_Transaction); ok {
		txn = a.transaction(t.Transaction)
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return err
	}
	if err := s.checkQuery(ctx, a, q, query.Shapes(aggs), req.GetExplainOptions() != nil); err != nil {
		return err
	}
	if nt, ok := req.GetConsistencySelector().(*pb.RunAggregationQueryRequest_NewTransaction); ok {
		id, end, err := s.newTransaction(ctx, a, nt.NewTransaction)
		if err != nil {
			return err
		}
		defer func() { end(err) }()
		ro.Transaction = a.transaction(id)
		var flush func() error
		send, flush = transactionFirst(send, &pb.RunAggregationQueryResponse{Transaction: id})
		defer func() {
			if err == nil {
				err = flush()
			}
		}()
	}
	ctx, cancel := context.WithTimeout(ctx, aggregationTimeout)
	defer cancel()
	agg := query.NewAggregator(aggs)
	_, readTime, err := s.runQuery(ctx, a, parent, q, ro, true, func(d *query.Doc, _ time.Time, _ int) error {
		if agg.Add(d) {
			return errAggregationDone
		}
		return nil
	})
	if err != nil {
		return err
	}
	return send(&pb.RunAggregationQueryResponse{Result: agg.ResultProto(), ReadTime: timestamppb.New(readTime)})
}

// PartitionQuery checks a partition query as the proto requires it (query.CheckPartition) and
// answers no partition: "An empty result may indicate that the query has too few results to be
// partitioned" (firestore.proto; ADR-025 §6), as production answered the recorded collection
// group of six documents (f0-descendants.json rest.partition.*).
func (s *Service) PartitionQuery(ctx context.Context, c Call, req *pb.PartitionQueryRequest) (*pb.PartitionQueryResponse, error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return nil, err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return nil, err
	}
	if err := query.CheckPartition(req, parent); err != nil {
		if query.PartitionShapeError(err) {
			// An order or a filter production does not partition by meets its index rule
			// first: __name__ descending needs a composite index, a field a collection group
			// index (f3-partition-query.json invalid.order_desc, order_field, filter_eq).
			if q, perr := query.Parse(req.GetStructuredQuery(), parent); perr == nil {
				if ierr := s.checkQuery(ctx, a, q, nil, false); ierr != nil {
					return nil, ierr
				}
			}
		}
		return nil, err
	}
	if _, err := readOptions(req.GetReadTime(), nil); err != nil {
		return nil, err
	}
	if _, err := s.o.Store.Database(ctx, a.db); err != nil {
		return nil, s.fail(ctx, err)
	}
	return &pb.PartitionQueryResponse{}, nil
}

// checkQuery checks a parsed query against its database, in production's order: the database
// exists; a find_nearest meets the vector indexes; the shape meets the composite-index rule
// (aggs are an aggregation query's, nil for a plain one); explain is refused.
func (s *Service) checkQuery(ctx context.Context, a admitted, q *query.Query, aggs []index.Aggregation, explain bool) error {
	mode, err := s.indexMode(ctx, a)
	if err != nil {
		return err
	}
	if q.FindNearest != nil {
		// TODO(F3 planner wave): the database's vector indexes, from the catalogue track B
		// stores; until then none is declared and every find_nearest is production's
		// missing-index answer.
		return q.NearestError(a.wdb.Project, nil)
	}
	cat, err := s.o.Store.IndexCatalog(ctx, a.db)
	if err != nil {
		return s.fail(ctx, err)
	}
	if err := s.enforce(a, q.Shape(aggs), cat, mode); err != nil {
		return err
	}
	if explain {
		return errExplain
	}
	return nil
}

// errExplain refuses explain_options (ADR-025 §5): production serves Query Explain, this server
// does not yet (parity exception explain-rejected).
var errExplain = wire.Unimplemented("Query Explain")

// indexMode is the database's composite-index enforcement: its own setting, else the server's
// (documents.index_enforcement). Reading it checks that the database exists.
func (s *Service) indexMode(ctx context.Context, a admitted) (index.Mode, error) {
	settings, err := s.o.Store.Database(ctx, a.db)
	if err != nil {
		return 0, s.fail(ctx, err)
	}
	if settings.IndexEnforcement != "" {
		if m, err := index.ParseMode(settings.IndexEnforcement); err == nil {
			return m, nil
		}
	}
	return s.o.IndexEnforcement, nil
}

// enforce decides a query's shape under mode: a needs-index query fails with production's
// error under enforce, and is served and logged under warn, as an uncertain verdict is
// (index_enforcement_uncertain, ADR-025 §4.2).
func (s *Service) enforce(a admitted, shape index.QueryShape, cat *index.Catalog, mode index.Mode) error {
	d := index.Check(shape, cat, mode)
	if d.Reject() {
		return wire.IndexError(index.Suggest(a.wdb.Project, a.wdb.ID, d))
	}
	if d.Log() {
		msg := "documents: index_enforcement_uncertain"
		if d.Verdict == index.NeedsIndex {
			msg = "documents: a query is served without the index production requires"
			if e := index.Suggest(a.wdb.Project, a.wdb.ID, d); e != nil {
				s.log.Warn(msg, "project", a.wdb.Project, "collection", shape.CollectionID, "index", e.Message)
				return nil
			}
		}
		s.log.Warn(msg, "project", a.wdb.Project, "collection", shape.CollectionID,
			"rule", string(d.Reason), "detail", d.Detail)
	}
	return nil
}

// runQuery runs q at ro: from an index when the planner serves it (runIndexed), else on the
// reference path: core's candidates, page by page in name order from
// the query's lower bound on __name__, to a query.Runner that sends results to emit with the
// read time and the results the offset skipped so far. unordered is an aggregation's read. It
// returns the results the offset skipped, and the read time.
func (s *Service) runQuery(ctx context.Context, a admitted, parent wire.Name, q *query.Query, ro core.ReadOptions, unordered bool,
	emit func(d *query.Doc, readTime time.Time, skipped int) error) (int, time.Time, error) {
	if skipped, readTime, ok, err := s.runIndexed(ctx, a, parent, q, ro, emit); ok || err != nil {
		return skipped, readTime, err
	}
	var readTime time.Time
	var runner *query.Runner
	runner = query.NewRunner(q, unordered, query.Limits{}, func(d *query.Doc) error { return emit(d, readTime, runner.Skipped()) })
	r := core.ScanRequest{
		Parent: pathOf(parent), CollectionID: q.CollectionID, AllDescendants: q.AllDescendants,
		PageSize: scanPage, ReadOptions: ro,
	}
	if lo, ok := q.Lower(); ok {
		if n, err := wire.ParseReference(lo.Name); err == nil && n.Database == a.wdb {
			r.From, r.FromInclusive = pathOf(n), lo.Inclusive
		}
	}
	err := func() error {
		for {
			res, err := s.o.Store.Scan(ctx, a.db, r)
			if err != nil {
				return s.fail(ctx, err)
			}
			readTime = res.ReadTime
			if r.Transaction == nil {
				// Every page reads the snapshot of the first.
				r.ReadTime = res.ReadTime
			}
			for _, d := range res.Documents {
				done, err := runner.Add(&query.Doc{Name: a.db.DocumentName(d.Path), Fields: d.Fields,
					CreateTime: d.CreateTime, UpdateTime: d.UpdateTime})
				if err != nil {
					return err
				}
				if done {
					return nil
				}
			}
			if res.Next == nil {
				return nil
			}
			r.From, r.FromInclusive = res.Next, false
		}
	}()
	if err == nil {
		err = runner.Finish()
	}
	if errors.Is(err, errAggregationDone) {
		err = nil
	}
	if err != nil {
		return 0, time.Time{}, s.fail(ctx, err)
	}
	query.Served(query.PathReference)
	return runner.Skipped(), readTime, nil
}

// queryDocument is a result as RunQuery sends it.
func queryDocument(d *query.Doc) *pb.Document {
	return wire.DocumentToProto(d.Name, d.Fields, timestamp(d.CreateTime), timestamp(d.UpdateTime))
}

// transactionFirst wraps a stream's send so that first, the response that carries a new
// transaction's id, goes before any other; flush sends it when nothing else did.
func transactionFirst[R any](send func(R) error, first R) (wrapped func(R) error, flush func() error) {
	pending := true
	wrapped = func(r R) error {
		if pending {
			pending = false
			if err := send(first); err != nil {
				return err
			}
		}
		return send(r)
	}
	flush = func() error {
		if !pending {
			return nil
		}
		pending = false
		return send(first)
	}
	return wrapped, flush
}
