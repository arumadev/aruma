package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// Hardening: the Document Database names its project in the URL, and again in the body of a
// request (document names, a query's parent, the database of a transaction). A caller of project
// A who puts B's names in a request to A's endpoint must get nothing of B's.
func TestHardDocumentsBodyNamesCannotCrossProjects(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	bDoc := "projects/" + h.b.id + "/databases/(default)/documents/c/d"
	bRoot := "projects/" + h.b.id + "/databases/(default)/documents"
	aBase := "/v1/projects/" + h.a.id + "/databases/(default)/documents"

	type call struct{ method, path, body string }
	calls := map[string]call{
		"commit write to B's document":                      {"POST", aBase + ":commit", `{"writes":[{"update":{"name":"` + bDoc + `","fields":{"v":{"stringValue":"pwned"}}}}]}`},
		"commit delete of B's document":                     {"POST", aBase + ":commit", `{"writes":[{"delete":"` + bDoc + `"}]}`},
		"batchWrite to B's document":                        {"POST", aBase + ":batchWrite", `{"writes":[{"update":{"name":"` + bDoc + `","fields":{"v":{"stringValue":"pwned"}}}}]}`},
		"batchGet of B's document":                          {"POST", aBase + ":batchGet", `{"documents":["` + bDoc + `"]}`},
		"runQuery with B's parent in the body":              {"POST", aBase + ":runQuery", `{"parent":"` + bRoot + `","structuredQuery":{"from":[{"collectionId":"c"}]}}`},
		"beginTransaction on B":                             {"POST", aBase + ":beginTransaction", `{"database":"projects/` + h.b.id + `/databases/(default)"}`},
		"commit of B's database":                            {"POST", aBase + ":commit", `{"database":"projects/` + h.b.id + `/databases/(default)","writes":[{"update":{"name":"` + bDoc + `","fields":{"v":{"stringValue":"pwned"}}}}]}`},
		"aggregation over B's parent":                       {"POST", aBase + ":runAggregationQuery", `{"parent":"` + bRoot + `","structuredAggregationQuery":{"structuredQuery":{"from":[{"collectionId":"c"}]},"aggregations":[{"count":{},"alias":"n"}]}}`},
		"listCollectionIds of B":                            {"POST", aBase + ":listCollectionIds", `{"parent":"` + bRoot + `"}`},
		"get B's document by A's path with encoded project": {"GET", "/v1/projects/" + h.a.id + "%2F..%2F" + h.b.id + "/databases/(default)/documents/c/d", ""},
		"get through dot segments":                          {"GET", "/v1/projects/" + h.a.id + "/../" + h.b.id + "/databases/(default)/documents/c/d", ""},
		"get with the project number":                       {"GET", "/v1/projects/" + h.b.number + "/databases/(default)/documents/c/d", ""},
	}
	for who, tok := range map[string]string{"A's service account": h.a.sat, "A's CLI token": h.a.pat, "A's user": h.a.idToken} {
		for name, c := range calls {
			code, body := documentsCall(t, h.Env, tok, c.method, c.path, c.body)
			if code == 200 && (strings.Contains(string(body), h.b.marker) || strings.Contains(string(body), "bravo-app")) {
				t.Errorf("%s, %s: %d %s", who, name, code, body)
			}
			if code == 200 && c.method == "POST" && strings.Contains(c.body, "pwned") {
				t.Errorf("%s, %s: the write was accepted: %s", who, name, body)
			}
		}
	}
	// B's document is as it was, and A's own read still works.
	code, body := documentsCall(t, h.Env, h.b.sat, "GET", hardDocPath(h.b.id), "")
	if code != 200 || !strings.Contains(string(body), h.b.marker) {
		t.Errorf("B's document changed: %d %s", code, body)
	}
	if code, body := documentsCall(t, h.Env, h.a.sat, "GET", aBase+"/c/d", ""); code != 200 || !strings.Contains(string(body), h.a.marker) {
		t.Errorf("control: A's own document: %d %s", code, body)
	}
}

// hardGRPC serves the entry point over h2c on a port of its own and returns a gRPC client of
// the Document Database for it.
func hardGRPC(t *testing.T, h *hardEnv) firestorepbconnect.FirestoreClient {
	t.Helper()
	var sp http.Protocols
	sp.SetHTTP1(true)
	sp.SetUnencryptedHTTP2(true)
	ts := httptest.NewUnstartedServer(h.App.Server.Handler())
	ts.Config.Protocols = &sp
	ts.Start()
	t.Cleanup(ts.Close)
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	return firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, ts.URL, connect.WithGRPC())
}

func withAuth[T any](r *connect.Request[T], token, resourcePrefix string) *connect.Request[T] {
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	if resourcePrefix != "" {
		r.Header().Set("Google-Cloud-Resource-Prefix", resourcePrefix)
		r.Header().Set("X-Goog-Request-Params", "database="+resourcePrefix)
	}
	return r
}

// Over gRPC, a caller of project A names B in the request (a document, a parent, the database of a
// transaction or a stream) while the resource prefix header says A: every such call is refused,
// B's document is unchanged, and A's own calls with the same credentials work.
func TestHardDocumentsGRPCCannotCrossProjects(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	client := hardGRPC(t, h)
	dbA, dbB := "projects/"+h.a.id+"/databases/(default)", "projects/"+h.b.id+"/databases/(default)"
	docB, docA := dbB+"/documents/c/d", dbA+"/documents/c/d"
	ctx := t.Context()
	write := func(name string) *pb.Write {
		return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: name, Fields: map[string]*pb.Value{"v": {ValueType: &pb.Value_StringValue{StringValue: "pwned"}}}}}}
	}
	drain := func(s interface {
		Receive() bool
		Msg() *pb.BatchGetDocumentsResponse
		Err() error
		Close() error
	}) (found []string, err error) {
		for s.Receive() {
			if d := s.Msg().GetFound(); d != nil {
				found = append(found, d.GetName()+" "+d.GetFields()["v"].GetStringValue())
			}
		}
		return found, s.Err()
	}
	for who, tok := range map[string]string{"A's service account": h.a.sat, "A's CLI token": h.a.pat, "A's user": h.a.idToken} {
		prefix := dbA
		// Reads. Each must fail, or answer with nothing of B's.
		if r, err := client.GetDocument(ctx, withAuth(connect.NewRequest(&pb.GetDocumentRequest{Name: docB}), tok, prefix)); err == nil {
			t.Errorf("%s: GetDocument of B's document: %v", who, r.Msg)
		}
		if s, err := client.BatchGetDocuments(ctx, withAuth(connect.NewRequest(&pb.BatchGetDocumentsRequest{Database: dbB, Documents: []string{docB}}), tok, prefix)); err == nil {
			if found, err := drain(s); err == nil && len(found) > 0 {
				t.Errorf("%s: BatchGetDocuments(database B) returned %v", who, found)
			}
		}
		if s, err := client.BatchGetDocuments(ctx, withAuth(connect.NewRequest(&pb.BatchGetDocumentsRequest{Database: dbA, Documents: []string{docB}}), tok, prefix)); err == nil {
			if found, err := drain(s); err == nil && len(found) > 0 {
				t.Errorf("%s: BatchGetDocuments(A's database, B's document) returned %v", who, found)
			}
		}
		if r, err := client.ListDocuments(ctx, withAuth(connect.NewRequest(&pb.ListDocumentsRequest{Parent: dbB + "/documents", CollectionId: "c"}), tok, prefix)); err == nil && len(r.Msg.Documents) > 0 {
			t.Errorf("%s: ListDocuments of B: %v", who, r.Msg)
		}
		if r, err := client.ListCollectionIds(ctx, withAuth(connect.NewRequest(&pb.ListCollectionIdsRequest{Parent: dbB + "/documents"}), tok, prefix)); err == nil && len(r.Msg.CollectionIds) > 0 {
			t.Errorf("%s: ListCollectionIds of B: %v", who, r.Msg)
		}
		q := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "c"}}}
		if s, err := client.RunQuery(ctx, withAuth(connect.NewRequest(&pb.RunQueryRequest{Parent: dbB + "/documents", QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: q}}), tok, prefix)); err == nil {
			for s.Receive() {
				if d := s.Msg().GetDocument(); d != nil && strings.Contains(d.GetName(), h.b.id) {
					t.Errorf("%s: RunQuery on B returned %s", who, d.GetName())
				}
				if d := s.Msg().GetDocument(); d != nil && d.GetFields()["v"].GetStringValue() == h.b.marker {
					t.Errorf("%s: RunQuery on B returned B's data", who)
				}
			}
		}
		// Writes.
		for _, c := range []*pb.CommitRequest{
			{Database: dbB, Writes: []*pb.Write{write(docB)}},
			{Database: dbA, Writes: []*pb.Write{write(docB)}},
		} {
			if _, err := client.Commit(ctx, withAuth(connect.NewRequest(c), tok, prefix)); err == nil {
				t.Errorf("%s: Commit(database %s) of B's document was accepted", who, c.Database)
			}
		}
		if r, err := client.BatchWrite(ctx, withAuth(connect.NewRequest(&pb.BatchWriteRequest{Database: dbA, Writes: []*pb.Write{write(docB)}}), tok, prefix)); err == nil {
			for _, st := range r.Msg.GetStatus() {
				if st.GetCode() == 0 {
					t.Errorf("%s: BatchWrite of B's document succeeded", who)
				}
			}
		}
		if _, err := client.UpdateDocument(ctx, withAuth(connect.NewRequest(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: docB, Fields: write(docB).GetUpdate().Fields}}), tok, prefix)); err == nil {
			t.Errorf("%s: UpdateDocument of B's document was accepted", who)
		}
		if _, err := client.DeleteDocument(ctx, withAuth(connect.NewRequest(&pb.DeleteDocumentRequest{Name: docB}), tok, prefix)); err == nil {
			t.Errorf("%s: DeleteDocument of B's document was accepted", who)
		}
		if _, err := client.CreateDocument(ctx, withAuth(connect.NewRequest(&pb.CreateDocumentRequest{Parent: dbB + "/documents", CollectionId: "c", DocumentId: "x", Document: &pb.Document{}}), tok, prefix)); err == nil {
			t.Errorf("%s: CreateDocument in B's database was accepted", who)
		}

		// Streams: Listen with B's document as a target, and Write with B's database in its first message.
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		l := client.Listen(sctx)
		l.RequestHeader().Set("Authorization", "Bearer "+tok)
		l.RequestHeader().Set("Google-Cloud-Resource-Prefix", prefix)
		l.RequestHeader().Set("X-Goog-Request-Params", "database="+prefix)
		_ = l.Send(&pb.ListenRequest{Database: dbB, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{TargetId: 1,
			TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: []string{docB}}}}}})
		for {
			m, err := l.Receive()
			if err != nil {
				break
			}
			if d := m.GetDocumentChange(); d != nil && strings.Contains(d.GetDocument().GetName(), h.b.id) {
				t.Errorf("%s: Listen(database B) delivered %s", who, d.GetDocument().GetName())
			}
			if m.GetTargetChange().GetTargetChangeType() == pb.TargetChange_CURRENT {
				break
			}
		}
		_ = l.CloseRequest()
		_ = l.CloseResponse()

		w := client.Write(sctx)
		w.RequestHeader().Set("Authorization", "Bearer "+tok)
		w.RequestHeader().Set("Google-Cloud-Resource-Prefix", prefix)
		w.RequestHeader().Set("X-Goog-Request-Params", "database="+prefix)
		_ = w.Send(&pb.WriteRequest{Database: dbB})
		if m, err := w.Receive(); err == nil && m.GetStreamId() != "" {
			_ = w.Send(&pb.WriteRequest{StreamId: m.GetStreamId(), StreamToken: m.GetStreamToken(), Writes: []*pb.Write{write(docB)}})
			if m2, err := w.Receive(); err == nil && len(m2.GetWriteResults()) > 0 {
				t.Errorf("%s: a Write stream opened on B's database wrote B's document", who)
			}
		}
		_ = w.CloseRequest()
		_ = w.CloseResponse()
	}

	// B's document is as it was.
	if code, body := documentsCall(t, h.Env, h.b.sat, "GET", hardDocPath(h.b.id), ""); code != 200 || !strings.Contains(string(body), h.b.marker) {
		t.Errorf("B's document changed: %d %s", code, body)
	}
	// Control: A's own calls with the same credentials work over the same client.
	if r, err := client.GetDocument(ctx, withAuth(connect.NewRequest(&pb.GetDocumentRequest{Name: docA}), h.a.sat, dbA)); err != nil || r.Msg.GetFields()["v"].GetStringValue() != h.a.marker {
		t.Errorf("control: A's own document over gRPC: %v %v", r, err)
	}
}
