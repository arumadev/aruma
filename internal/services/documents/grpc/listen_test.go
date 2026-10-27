package grpc_test

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The Listen stream in process, every frame checked by apitest.ListenChecker. The shapes are
// production's (f4-listen.json), which test/differential/documents replays case by case; these
// tests add what the corpus could not record: several targets and commits at once, windows
// refilled, resumes compared with the store, the hub's resync.

func rankDoc(n int64) map[string]*pb.Value { return map[string]*pb.Value{"rank": intValue(n)} }

func docTarget(id int32, paths ...string) *pb.Target {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = root + "/" + p
	}
	return &pb.Target{TargetId: id, TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: names}}}
}

func queryTarget(id int32, sq *pb.StructuredQuery) *pb.Target {
	return &pb.Target{TargetId: id, TargetType: &pb.Target_Query{Query: &pb.Target_QueryTarget{Parent: root,
		QueryType: &pb.Target_QueryTarget_StructuredQuery{StructuredQuery: sq}}}}
}

// byRank is collection coll ordered by rank, from rank min when min >= 0, with limit and offset
// when set.
func byRank(coll string, min int64, limit, offset int32) *pb.StructuredQuery {
	sq := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: coll}},
		OrderBy: []*pb.StructuredQuery_Order{{Field: &pb.StructuredQuery_FieldReference{FieldPath: "rank"}, Direction: pb.StructuredQuery_ASCENDING}},
		Offset:  offset}
	if min >= 0 {
		sq.Where = &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
			Field: &pb.StructuredQuery_FieldReference{FieldPath: "rank"}, Op: pb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL, Value: intValue(min)}}}
	}
	if limit > 0 {
		sq.Limit = wrapperspb.Int32(limit)
	}
	return sq
}

func del(path string) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Delete{Delete: root + "/" + path}}
}

// commit commits writes and returns the commit time.
func (x *harness) commit(t *testing.T, tok string, writes ...*pb.Write) time.Time {
	t.Helper()
	res, err := x.client.Commit(t.Context(), req(&pb.CommitRequest{Database: db, Writes: writes}, tok))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetCommitTime().AsTime()
}

// shape is a frame in short: kinds, relative names and ids, as the tests compare them.
func shape(r *pb.ListenResponse) string {
	rel := func(n string) string { return strings.TrimPrefix(n, root+"/") }
	switch x := r.GetResponseType().(type) {
	case *pb.ListenResponse_TargetChange:
		tc := x.TargetChange
		if len(tc.GetTargetIds()) == 0 && tc.GetTargetChangeType() == pb.TargetChange_NO_CHANGE {
			return "global"
		}
		s := fmt.Sprintf("%v%v", tc.GetTargetChangeType(), tc.GetTargetIds())
		if c := tc.GetCause(); c != nil {
			s += fmt.Sprintf(" %d %q", c.GetCode(), c.GetMessage())
		}
		return s
	case *pb.ListenResponse_DocumentChange:
		dc := x.DocumentChange
		s := fmt.Sprintf("change %s %v", rel(dc.GetDocument().GetName()), dc.GetTargetIds())
		if len(dc.GetRemovedTargetIds()) > 0 {
			s += fmt.Sprintf(" -%v", dc.GetRemovedTargetIds())
		}
		return s
	case *pb.ListenResponse_DocumentDelete:
		return fmt.Sprintf("delete %s -%v", rel(x.DocumentDelete.GetDocument()), x.DocumentDelete.GetRemovedTargetIds())
	case *pb.ListenResponse_DocumentRemove:
		return fmt.Sprintf("remove %s -%v", rel(x.DocumentRemove.GetDocument()), x.DocumentRemove.GetRemovedTargetIds())
	case *pb.ListenResponse_Filter:
		return fmt.Sprintf("filter[%d] %d", x.Filter.GetTargetId(), x.Filter.GetCount())
	}
	return "?"
}

func shapes(rs []*pb.ListenResponse) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = shape(r)
	}
	return out
}

func wantShapes(t *testing.T, what string, got []*pb.ListenResponse, want ...string) {
	t.Helper()
	if g := shapes(got); !slices.Equal(g, want) {
		t.Errorf("%s:\n got  %q\n want %q", what, g, want)
	}
}

func TestListenTargets(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("lt/a", rankDoc(1)), update("lt/b", rankDoc(2)), update("lt/c", rankDoc(3)))
	l := apitest.OpenListen(t, x.client, tok)

	l.Add(docTarget(1, "lt/a"))
	wantShapes(t, "document target", l.UntilGlobal(), "ADD[1]", "change lt/a [1]", "CURRENT[1]", "global")
	l.Add(docTarget(2, "lt/none", "lt/a"))
	wantShapes(t, "a missing document", l.UntilGlobal(), "ADD[2]", "delete lt/none -[2]", "change lt/a [2]", "CURRENT[2]", "global")
	l.Add(queryTarget(3, byRank("lt", 2, 0, 0)))
	wantShapes(t, "query target", l.UntilGlobal(), "ADD[3]", "change lt/b [3]", "change lt/c [3]", "CURRENT[3]", "global")

	// One message per document for every target; a leave carries the new body; whole commits.
	x.commit(t, tok, update("lt/a", rankDoc(5)))
	wantShapes(t, "enters", l.UntilGlobal(), "change lt/a [1 2 3]", "global")
	x.commit(t, tok, update("lt/b", rankDoc(0)))
	got := l.UntilGlobal()
	wantShapes(t, "leaves by a change", got, "change lt/b [] -[3]", "global")
	if got[0].GetDocumentChange().GetDocument().GetFields()["rank"].GetIntegerValue() != 0 {
		t.Errorf("a leave carries the old body: %v", got[0])
	}
	x.commit(t, tok, del("lt/c"), update("lt/none", rankDoc(9)), update("lt/zz", rankDoc(-1)))
	wantShapes(t, "one commit", l.UntilGlobal(), "delete lt/c -[3]", "change lt/none [2 3]", "global")
	x.commit(t, tok, del("lt/a"))
	wantShapes(t, "delete", l.UntilGlobal(), "delete lt/a -[1 2 3]", "global")
	if g := l.Checker.Target(3).Docs; len(g) != 1 {
		t.Errorf("target 3 holds %d documents, want lt/none", len(g))
	}

	// remove, then the same id again; a commit of no target is not sent.
	l.Remove(2)
	wantShapes(t, "remove", l.Until(func(*pb.ListenResponse) bool { return true }), "REMOVE[2]")
	x.commit(t, tok, update("lt/zz", rankDoc(-2)))
	l.Quiet(200 * time.Millisecond)
	l.Add(docTarget(2, "lt/none"))
	wantShapes(t, "re-add", l.UntilGlobal(), "ADD[2]", "change lt/none [2]", "CURRENT[2]", "global")

	// once: the result, CURRENT, the global snapshot, REMOVE.
	once := docTarget(4, "lt/none")
	once.Once = true
	l.Add(once)
	wantShapes(t, "once", l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, 4) }),
		"ADD[4]", "change lt/none [4]", "CURRENT[4]", "global", "REMOVE[4]")
	l.CloseSend()
	if err := l.Ended(); err != nil {
		t.Errorf("a client that ends its side: %v", err)
	}
}

func TestListenRefusals(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("rf/p", intField(1)))
	l := apitest.OpenListen(t, x.client, tok)
	one := func(id int32) []*pb.ListenResponse {
		return l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, id) })
	}

	// Refused before ADD; the stream stays open.
	sel := byRank("rf", -1, 0, 0)
	sel.Select = &pb.StructuredQuery_Projection{}
	l.Add(queryTarget(5, sel))
	wantShapes(t, "select", one(5), `REMOVE[5] 3 "'select' clauses are not supported for real-time queries."`)
	l.Add(&pb.Target{TargetId: 6, TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{
		Documents: []string{other + "/documents/rf/p"}}}})
	wantShapes(t, "another project", one(6), `REMOVE[6] 3 "Cannot add a target for a different database."`)
	l.Add(&pb.Target{TargetId: 7, ResumeType: &pb.Target_ResumeToken{ResumeToken: []byte("not-a-token")},
		TargetType: queryTarget(7, byRank("rf", -1, 0, 0)).TargetType})
	wantShapes(t, "bad token", one(7), `REMOVE[7] 3 "bad resume token"`)

	// Refused after ADD: what the database decides.
	desc := byRank("rf", -1, 0, 0)
	desc.OrderBy = []*pb.StructuredQuery_Order{{Field: &pb.StructuredQuery_FieldReference{FieldPath: "__name__"}, Direction: pb.StructuredQuery_DESCENDING}}
	l.Add(queryTarget(8, desc))
	if got := shapes(one(8)); len(got) != 2 || got[0] != "ADD[8]" || !strings.HasPrefix(got[1], `REMOVE[8] 9 "The query requires an index.`) {
		t.Errorf("missing index: %q", got)
	}
	future := queryTarget(9, byRank("rf", -1, 0, 0))
	future.ResumeType = &pb.Target_ReadTime{ReadTime: timestamppb.New(time.Now().Add(time.Hour))}
	l.Add(future)
	wantShapes(t, "future read_time", one(9), "ADD[9]", `REMOVE[9] 3 "The requested 'read_time' cannot be in the future."`)

	// The stream is still open: server-assigned ids are max+1.
	l.Add(docTarget(0, "rf/p"))
	wantShapes(t, "assigned id", l.UntilGlobal(), "ADD[10]", "change rf/p [10]", "CURRENT[10]", "global")

	// The client's mistakes in the stream end it.
	for _, c := range []struct {
		name   string
		header []string
		reqs   []*pb.ListenRequest
		code   connect.Code
		msg    string
	}{
		{"negative id", nil, []*pb.ListenRequest{{TargetChange: &pb.ListenRequest_AddTarget{AddTarget: docTarget(-1, "rf/p")}}},
			connect.CodeInvalidArgument, "Invalid target ID: -1"},
		{"no target change", nil, []*pb.ListenRequest{{}}, connect.CodeInvalidArgument, "Unknown target_change: 0"},
		{"another database than the header's", []string{"Google-Cloud-Resource-Prefix", "projects/" + apitest.Project + "/databases/other"},
			[]*pb.ListenRequest{{TargetChange: &pb.ListenRequest_AddTarget{AddTarget: docTarget(1, "rf/p")}}}, connect.CodeInvalidArgument,
			"Mismatch between database name in the request and http header ('google-cloud-resource-prefix' or 'x-goog-request-params') or query param 'database'."},
		{"unknown remove", nil, []*pb.ListenRequest{{TargetChange: &pb.ListenRequest_RemoveTarget{RemoveTarget: 99}}},
			connect.CodeNotFound, "Target id not found: 99"},
		{"manual id after an assigned one", nil, []*pb.ListenRequest{
			{TargetChange: &pb.ListenRequest_AddTarget{AddTarget: docTarget(0, "rf/p")}},
			{TargetChange: &pb.ListenRequest_AddTarget{AddTarget: docTarget(7, "rf/p")}}},
			connect.CodeInvalidArgument, "Manually assigned target IDs are not allowed after a server assigned ID."},
	} {
		s := apitest.OpenListen(t, x.client, tok, c.header...)
		for i, r := range c.reqs {
			s.Send(r)
			if i < len(c.reqs)-1 {
				s.UntilGlobal()
			}
		}
		wantErr(t, c.name, s.Ended(), c.code, c.msg)
	}
}

// settled fails unless the stream holds every target CURRENT and no answer is pending.
func settled(t *testing.T, l *apitest.ListenClient) {
	t.Helper()
	if !l.Checker.Settled() {
		t.Fatal("listen: requests left unanswered")
	}
}

func TestListenWindows(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("lw/r1", rankDoc(1)), update("lw/r2", rankDoc(2)), update("lw/r3", rankDoc(3)), update("lw/r4", rankDoc(4)))
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(queryTarget(1, byRank("lw", -1, 2, 0)))
	wantShapes(t, "limit", l.UntilGlobal(), "ADD[1]", "change lw/r1 [1]", "change lw/r2 [1]", "CURRENT[1]", "global")
	l.Add(queryTarget(2, byRank("lw", -1, 0, 1)))
	wantShapes(t, "offset", l.UntilGlobal(), "ADD[2]", "change lw/r2 [2]", "change lw/r3 [2]", "change lw/r4 [2]", "CURRENT[2]", "global")

	// A member deleted: RESET and the window (limit-target).
	x.commit(t, tok, del("lw/r1"))
	wantShapes(t, "member deleted", l.UntilGlobal(),
		"RESET[1]", "change lw/r2 [1]", "change lw/r3 [1]", "CURRENT[1]",
		"RESET[2]", "change lw/r3 [2]", "change lw/r4 [2]", "CURRENT[2]", "global")
	// One that enters at the front pushes the last out.
	x.commit(t, tok, update("lw/r0", rankDoc(0)))
	wantShapes(t, "enters at the front", l.UntilGlobal(), "remove lw/r3 -[1]", "change lw/r0 [1]", "change lw/r2 [2]", "global")
	// A change behind the window is nothing to it; one in it is a change.
	x.commit(t, tok, update("lw/r4", map[string]*pb.Value{"rank": intValue(4), "note": intValue(1)}))
	wantShapes(t, "behind", l.UntilGlobal(), "change lw/r4 [2]", "global")
	x.commit(t, tok, update("lw/r2", map[string]*pb.Value{"rank": intValue(2), "note": intValue(1)}))
	wantShapes(t, "in it", l.UntilGlobal(), "change lw/r2 [1 2]", "global")
	// A member moved behind the window.
	x.commit(t, tok, update("lw/r0", rankDoc(50)))
	wantShapes(t, "moved out", l.UntilGlobal(), "RESET[1]", "change lw/r2 [1]", "change lw/r3 [1]", "CURRENT[1]",
		"RESET[2]", "change lw/r3 [2]", "change lw/r4 [2]", "change lw/r0 [2]", "CURRENT[2]", "global")
	settled(t, l)
}

func TestListenResume(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("rs/x1", rankDoc(1)), update("rs/x2", rankDoc(2)), update("rs/x3", rankDoc(3)))
	l := apitest.OpenListen(t, x.client, tok)
	for _, tg := range []*pb.Target{queryTarget(1, byRank("rs", 1, 0, 0)), queryTarget(2, byRank("rs", 1, 2, 0)), docTarget(3, "rs/x3")} {
		l.Add(tg)
		l.UntilGlobal()
	}
	t0 := l.Checker.Target(1).Token
	l.CloseSend()
	l.Ended()

	// While away: one enters, one leaves by a change, one is deleted.
	x.commit(t, tok, update("rs/x4", rankDoc(4)))
	x.commit(t, tok, update("rs/x2", rankDoc(0)))
	x.commit(t, tok, del("rs/x3"))

	resumed := func(tg *pb.Target, expected int32) *pb.Target {
		tg.ResumeType = &pb.Target_ResumeToken{ResumeToken: t0}
		if expected >= 0 {
			tg.ExpectedCount = wrapperspb.Int32(expected)
		}
		return tg
	}
	l = apitest.OpenListen(t, x.client, tok)
	l.Add(resumed(queryTarget(1, byRank("rs", 1, 0, 0)), 3))
	wantShapes(t, "count path", l.UntilGlobal(), "ADD[1]", "change rs/x4 [1]", "delete rs/x3 -[1]", "filter[1] 2", "CURRENT[1]", "global")
	l.Add(resumed(queryTarget(2, byRank("rs", 1, 2, 0)), -1))
	wantShapes(t, "limit, a member left", l.UntilGlobal(), "ADD[2]", "RESET[2]", "change rs/x1 [2]", "change rs/x4 [2]", "CURRENT[2]", "global")
	l.Add(resumed(docTarget(3, "rs/x3"), -1))
	wantShapes(t, "document target", l.UntilGlobal(), "ADD[3]", "delete rs/x3 -[3]", "CURRENT[3]", "global")

	// The client agrees: no filter. Nothing changed in the limit window: CURRENT alone.
	t1 := l.Checker.Target(1).Token
	l = apitest.OpenListen(t, x.client, tok)
	l.Add(&pb.Target{TargetId: 1, ResumeType: &pb.Target_ResumeToken{ResumeToken: t1}, ExpectedCount: wrapperspb.Int32(2),
		TargetType: queryTarget(1, byRank("rs", 1, 0, 0)).TargetType})
	wantShapes(t, "nothing changed", l.UntilGlobal(), "ADD[1]", "CURRENT[1]", "global")
	l.Add(&pb.Target{TargetId: 2, ResumeType: &pb.Target_ResumeToken{ResumeToken: t1}, TargetType: queryTarget(2, byRank("rs", 1, 2, 0)).TargetType})
	wantShapes(t, "window unchanged", l.UntilGlobal(), "ADD[2]", "CURRENT[2]", "global")

	// Older than the versions kept: the count, no deletes; a limit target RESET.
	old := timestamppb.New(time.Now().Add(-3 * time.Hour))
	l.Add(&pb.Target{TargetId: 4, ResumeType: &pb.Target_ReadTime{ReadTime: old}, TargetType: queryTarget(4, byRank("rs", 1, 0, 0)).TargetType})
	wantShapes(t, "old read_time", l.UntilGlobal(), "ADD[4]", "change rs/x1 [4]", "change rs/x4 [4]", "filter[4] 2", "CURRENT[4]", "global")
	l.Add(&pb.Target{TargetId: 5, ResumeType: &pb.Target_ReadTime{ReadTime: old}, TargetType: queryTarget(5, byRank("rs", 1, 2, 0)).TargetType})
	wantShapes(t, "old read_time, limit", l.UntilGlobal(), "ADD[5]", "RESET[5]", "change rs/x1 [5]", "change rs/x4 [5]", "CURRENT[5]", "global")
	settled(t, l)
}

// dropOne loses the next commit it is told of once armed: the hub fills the gap from the
// changelog after its GapTimeout, releasing the commits held behind it at once.
type dropOne struct {
	documents.Notifier
	armed atomic.Bool
}

func (d *dropOne) Committed(db documents.DB, seq int64, at time.Time, changes []documents.Change) {
	if d.armed.CompareAndSwap(true, false) {
		return
	}
	d.Notifier.Committed(db, seq, at, changes)
}

// TestListenResync: a stream that falls behind the hub (more than LagEvents events at once)
// has its events dropped; it answers with RESET and every target's result, never with a gap.
func TestListenResync(t *testing.T) {
	drop := &dropOne{}
	x := newHarness(t, apitest.Options{Hub: listen.Options{LagEvents: 2, GapTimeout: 500 * time.Millisecond},
		Notifier: func(n documents.Notifier) documents.Notifier { drop.Notifier = n; return drop }}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("ry/a", rankDoc(1)))
	l := apitest.OpenListen(t, x.client, tok)
	l.Checker.Oracle = func(id int32, rt time.Time) (map[string]time.Time, error) {
		return x.result(t, rt, byRank("ry", -1, 0, 0))
	}
	l.Add(queryTarget(1, byRank("ry", -1, 0, 0)))
	l.UntilGlobal()
	// The first commit is lost; the hub holds the rest behind it, then releases them together.
	drop.armed.Store(true)
	for i := range 24 {
		x.commit(t, tok, update(fmt.Sprintf("ry/d%02d", i), rankDoc(int64(i))))
	}
	last := x.commit(t, tok, update("ry/a", rankDoc(2)))
	sawReset := false
	for {
		res := l.Next(apitest.ListenWait)
		if res == nil {
			t.Fatalf("no global snapshot at the last commit (end %v)", l.End)
		}
		sawReset = sawReset || apitest.IsTargetChange(res, pb.TargetChange_RESET, 1)
		if apitest.IsGlobal(res) && !res.GetTargetChange().GetReadTime().AsTime().Before(last) {
			break
		}
	}
	if !sawReset {
		t.Error("the stream never fell behind: no RESET")
	}
	if n := len(l.Checker.Target(1).Docs); n != 25 {
		t.Errorf("after the resync the target holds %d documents, want 25", n)
	}
}

// result is a query's result at a read time, by name with update times: the oracle the checker
// compares each global snapshot with.
func (x *harness) result(t *testing.T, rt time.Time, sq *pb.StructuredQuery) (map[string]time.Time, error) {
	st, err := x.client.RunQuery(t.Context(), req(&pb.RunQueryRequest{Parent: root,
		QueryType:           &pb.RunQueryRequest_StructuredQuery{StructuredQuery: sq},
		ConsistencySelector: &pb.RunQueryRequest_ReadTime{ReadTime: timestamppb.New(rt)}}, x.env.JWT(t)))
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for st.Receive() {
		if d := st.Msg().GetDocument(); d != nil {
			out[d.GetName()] = d.GetUpdateTime().AsTime()
		}
	}
	return out, st.Err()
}
