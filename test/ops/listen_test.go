package ops

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// grpcClient is a Firestore gRPC client of a deployment, which speaks h2c on its one port.
func grpcClient(base string) firestorepbconnect.FirestoreClient {
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	return firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, base, connect.WithGRPC())
}

// listener is one Listen stream with one query target on the cities with country US.
type listener struct {
	t      *testing.T
	stream *connect.BidiStreamForClient[pb.ListenRequest, pb.ListenResponse]
	cancel context.CancelFunc
	ch     chan *pb.ListenResponse
}

func openListen(t *testing.T, base string, p *project, pat string, resume []byte, expected int32) *listener {
	t.Helper()
	db := "projects/" + p.ID + "/databases/(default)"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	s := grpcClient(base).Listen(ctx)
	s.RequestHeader().Set("Authorization", "Bearer "+pat)
	s.RequestHeader().Set("Google-Cloud-Resource-Prefix", db)
	s.RequestHeader().Set("X-Goog-Request-Params", "database="+db)
	target := &pb.Target{TargetId: 1, TargetType: &pb.Target_Query{Query: &pb.Target_QueryTarget{
		Parent: db + "/documents",
		QueryType: &pb.Target_QueryTarget_StructuredQuery{StructuredQuery: &pb.StructuredQuery{
			From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "cities"}},
			Where: &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
				Field: &pb.StructuredQuery_FieldReference{FieldPath: "country"}, Op: pb.StructuredQuery_FieldFilter_EQUAL,
				Value: &pb.Value{ValueType: &pb.Value_StringValue{StringValue: "US"}}}}},
		}}}}}
	if resume != nil {
		target.ResumeType = &pb.Target_ResumeToken{ResumeToken: resume}
		target.ExpectedCount = wrapperspb.Int32(expected)
	}
	if err := s.Send(&pb.ListenRequest{Database: db, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: target}}); err != nil {
		t.Fatalf("listen: %v", err)
	}
	l := &listener{t: t, stream: s, cancel: cancel, ch: make(chan *pb.ListenResponse, 100)}
	go func() {
		defer close(l.ch)
		for {
			m, err := s.Receive()
			if err != nil {
				return
			}
			l.ch <- m
		}
	}()
	t.Cleanup(l.close)
	return l
}

func (l *listener) close() { l.cancel() }

// until reads messages until fn says stop, or fails after the timeout.
func (l *listener) until(what string, fn func(*pb.ListenResponse) bool) {
	l.t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case m, ok := <-l.ch:
			if !ok {
				l.t.Fatalf("listen: the stream ended before %s", what)
			}
			if fn(m) {
				return
			}
		case <-timeout:
			l.t.Fatalf("listen: no %s in 15 seconds", what)
		}
	}
}

// TestRestoreListenResume: a listener that resumes with a token from after the backup, on a
// server restored from the backup, must be told that its cache holds a document the server no
// longer has: the existence filter (or a reset) answers the expected count the SDKs send.
func TestRestoreListenResume(t *testing.T) {
	needPGTools(t)
	d := newDeployment(t, headBinary(t), "src")
	d.mustRun("", "migrate", "up")
	d.mustStart()
	fx := populate(t, d)
	p := fx.Projects[0]

	var cache = map[string]bool{}
	var token []byte
	l := openListen(t, d.base(), p, fx.PAT, nil, 0)
	l.until("the initial snapshot", func(m *pb.ListenResponse) bool {
		if c := m.GetDocumentChange(); c != nil {
			cache[c.GetDocument().GetName()] = true
		}
		if tc := m.GetTargetChange(); tc != nil && tc.GetTargetChangeType() == pb.TargetChange_CURRENT {
			token = tc.GetResumeToken()
		}
		return token != nil
	})
	if len(cache) != 2 {
		t.Fatalf("the query holds %d documents, want 2", len(cache))
	}

	b := takeBackup(t, d, t.TempDir())

	// A document the backup does not hold; the listener sees it and moves its token.
	name := "projects/" + p.ID + "/databases/(default)/documents/cities/chicago"
	a := newAPI(t, d.base())
	a.must("POST", docsBase(p)+"/documents:commit", bearer(fx.PAT), map[string]any{"writes": []any{map[string]any{"update": map[string]any{"name": name,
		"fields": map[string]any{"country": map[string]any{"stringValue": "US"}, "pop": map[string]any{"integerValue": "2700000"}}}}}})
	var later []byte
	l.until("the new document", func(m *pb.ListenResponse) bool {
		if c := m.GetDocumentChange(); c != nil && c.GetDocument().GetName() == name {
			cache[name] = true
		}
		if tc := m.GetTargetChange(); tc != nil && len(tc.GetResumeToken()) > 0 && cache[name] {
			later = tc.GetResumeToken()
		}
		return later != nil
	})
	d.stop()

	r := restoreInto(t, d, b, d.key)
	r.mustStart()

	// The SDK resumes with its token and the number of documents it holds.
	l2 := openListen(t, r.base(), p, fx.PAT, later, int32(len(cache)))
	var sawFilter, sawReset, sawDoc bool
	l2.until("the resumed target to be current", func(m *pb.ListenResponse) bool {
		if f := m.GetFilter(); f != nil {
			sawFilter = true
			if int(f.GetCount()) != 2 {
				t.Errorf("the existence filter says %d documents, the restored query has 2", f.GetCount())
			}
		}
		if m.GetDocumentChange() != nil {
			sawDoc = true
		}
		tc := m.GetTargetChange()
		if tc != nil && tc.GetTargetChangeType() == pb.TargetChange_RESET {
			sawReset = true
		}
		return tc != nil && tc.GetTargetChangeType() == pb.TargetChange_CURRENT
	})
	if !sawFilter && !sawReset {
		t.Errorf("a client resuming with a token from after the backup, holding 3 documents where the restored database has 2, is told nothing: no existence filter and no reset (documents sent: %v)", sawDoc)
	}
	// A fresh listener sees the restored state, and a write after the restore reaches it.
	l3 := openListen(t, r.base(), p, fx.PAT, nil, 0)
	got := map[string]bool{}
	cur := false
	l3.until("the snapshot", func(m *pb.ListenResponse) bool {
		if c := m.GetDocumentChange(); c != nil {
			got[c.GetDocument().GetName()] = true
		}
		cur = cur || m.GetTargetChange().GetTargetChangeType() == pb.TargetChange_CURRENT
		return cur
	})
	if len(got) != 2 || got[name] {
		t.Errorf("a fresh listener on the restored database sees %v", got)
	}
	ra := newAPI(t, r.base())
	ra.must("POST", docsBase(p)+"/documents:commit", bearer(fx.PAT), map[string]any{"writes": []any{map[string]any{"update": map[string]any{"name": name,
		"fields": map[string]any{"country": map[string]any{"stringValue": "US"}, "pop": map[string]any{"integerValue": "1"}}}}}})
	l3.until("the write after the restore", func(m *pb.ListenResponse) bool {
		return m.GetDocumentChange() != nil && m.GetDocumentChange().GetDocument().GetName() == name
	})
}
