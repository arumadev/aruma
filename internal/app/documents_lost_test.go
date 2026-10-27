package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// The single-instance lock lost (ADR-026 §5): every Listen and Write stream closes UNAVAILABLE;
// once the lock is taken again, a new hub serves new streams and the commits reach them.
func TestDocumentsInstanceLost(t *testing.T) {
	t.Setenv("ARUMA_TEST_REQUIRE_DB", "1")
	url := storetest.NewDB(t)
	env := map[string]string{
		"ARUMA_DATABASE_URL":              url,
		"ARUMA_PUBLIC_URL":                "http://127.0.0.1:8080",
		"ARUMA_LISTEN":                    "127.0.0.1:8080",
		"ARUMA_MASTER_KEY":                "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"ARUMA_DOCUMENTS_ENABLED":         "true",
		"ARUMA_DOCUMENTS_DEV_OWNER_TOKEN": "true",
	}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	pool, err := store.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool, WithArgon2(crypto.Argon2Params{Memory: 64, Time: 1, Threads: 1}))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'owner@example.com', 'x')`, id); err != nil {
		t.Fatal(err)
	}
	owner := &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodSession, AccountID: id}
	if _, err := a.Service.CreateProject(t.Context(), owner, core.NewProject{ID: "lost-app", DisplayName: "lost"}); err != nil {
		t.Fatal(err)
	}
	var lost, acquired atomic.Int32
	if err := a.Documents.Boot(t.Context(), documents.InstanceOptions{
		Check: 50 * time.Millisecond, MinBackoff: 50 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OnLost: func() { lost.Add(1) }, OnAcquired: func() { acquired.Add(1) },
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Documents.Close)
	ts := httptest.NewUnstartedServer(a.Server.Handler())
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	ts.Config.Protocols = &p
	ts.Start()
	t.Cleanup(ts.Close)
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	client := firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, ts.URL, connect.WithGRPC())

	const database = "projects/lost-app/databases/(default)"
	listen := func() *apitest.ListenClient {
		l := apitest.OpenListen(t, client, "owner")
		l.Send(&pb.ListenRequest{Database: database, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{TargetId: 1,
			TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: []string{database + "/documents/c/d"}}}}}})
		l.UntilGlobal()
		return l
	}
	l := listen()
	w := client.Write(t.Context())
	w.RequestHeader().Set("Authorization", "Bearer owner")
	t.Cleanup(func() { _ = w.CloseRequest(); _ = w.CloseResponse() })
	if err := w.Send(&pb.WriteRequest{Database: database}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Receive(); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(pg_terminate_backend(pid)) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("terminated %d holders: %v", n, err)
	}
	if err := l.Ended(); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Listen at the lock's loss: %v", err)
	}
	if _, err := w.Receive(); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Write at the lock's loss: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for acquired.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the lock was not taken again (lost %d)", lost.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}

	l = listen()
	cr := connect.NewRequest(&pb.CommitRequest{Database: database, Writes: []*pb.Write{{Operation: &pb.Write_Update{
		Update: &pb.Document{Name: database + "/documents/c/d"}}}}})
	cr.Header().Set("Authorization", "Bearer owner")
	if _, err := client.Commit(t.Context(), cr); err != nil {
		t.Fatal(err)
	}
	if got := l.UntilGlobal(); len(got) != 2 || got[0].GetDocumentChange() == nil {
		t.Errorf("a commit on the rebuilt hub: %v", got)
	}
}
