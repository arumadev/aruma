package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

// lostApp is an App with the Document Database on, a project "lost-app" its owner holds, and
// its URL to open other sessions on.
func lostApp(t *testing.T) (*App, *pgxpool.Pool, string) {
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
	return a, pool, url
}

// lostClient serves a over HTTP/2 and returns a gRPC client of it.
func lostClient(t *testing.T, a *App) firestorepbconnect.FirestoreClient {
	ts := httptest.NewUnstartedServer(a.Server.Handler())
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	ts.Config.Protocols = &p
	ts.Start()
	t.Cleanup(ts.Close)
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	return firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, ts.URL, connect.WithGRPC())
}

const lostDatabase = "projects/lost-app/databases/(default)"

// lostCommit commits c/d as {n: n}, as the owner.
func lostCommit(t *testing.T, client firestorepbconnect.FirestoreClient, n int64) error {
	cr := connect.NewRequest(&pb.CommitRequest{Database: lostDatabase, Writes: []*pb.Write{{Operation: &pb.Write_Update{
		Update: &pb.Document{Name: lostDatabase + "/documents/c/d",
			Fields: map[string]*pb.Value{"n": {ValueType: &pb.Value_IntegerValue{IntegerValue: n}}}}}}}})
	cr.Header().Set("Authorization", "Bearer owner")
	_, err := client.Commit(t.Context(), cr)
	return err
}

// takeLock takes the single-instance lock on a session of its own, held until the test ends.
func takeLock(t *testing.T, url string) {
	other, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	var ok bool
	if err := other.QueryRow(t.Context(), "SELECT pg_try_advisory_lock(hashtext('documents.instance'), 0)").Scan(&ok); err != nil || !ok {
		t.Fatalf("another session took the lock: %v, %v", ok, err)
	}
}

// terminateHolder ends the session that holds the single-instance lock.
func terminateHolder(t *testing.T, pool *pgxpool.Pool) {
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(pg_terminate_backend(pid)) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("terminated %d holders: %v", n, err)
	}
}

// The single-instance lock lost (ADR-026 §5): every Listen and Write stream closes UNAVAILABLE;
// once the lock is taken again, a new hub serves new streams and the commits reach them.
func TestDocumentsInstanceLost(t *testing.T) {
	a, pool, _ := lostApp(t)
	var lost, acquired atomic.Int32
	if err := a.Documents.Boot(t.Context(), documents.InstanceOptions{
		Check: 50 * time.Millisecond, MinBackoff: 50 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OnLost: func() { lost.Add(1) }, OnAcquired: func() { acquired.Add(1) },
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Documents.Close)
	client := lostClient(t, a)

	listen := func() *apitest.ListenClient {
		l := apitest.OpenListen(t, client, "owner")
		l.Send(&pb.ListenRequest{Database: lostDatabase, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{TargetId: 1,
			TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: []string{lostDatabase + "/documents/c/d"}}}}}})
		l.UntilGlobal()
		return l
	}
	l := listen()
	w := client.Write(t.Context())
	w.RequestHeader().Set("Authorization", "Bearer owner")
	t.Cleanup(func() { _ = w.CloseRequest(); _ = w.CloseResponse() })
	if err := w.Send(&pb.WriteRequest{Database: lostDatabase}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Receive(); err != nil {
		t.Fatal(err)
	}

	terminateHolder(t, pool)
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
	if err := lostCommit(t, client, 1); err != nil {
		t.Fatal(err)
	}
	if got := l.UntilGlobal(); len(got) != 2 || got[0].GetDocumentChange() == nil {
		t.Errorf("a commit on the rebuilt hub: %v", got)
	}
}

// A commit after another session took the lock fails UNAVAILABLE, though the guard has not
// checked the lock since (ADR-024 §7, review lock-loss-commit-window).
func TestDocumentsCommitAfterTakeover(t *testing.T) {
	a, pool, url := lostApp(t)
	var lost atomic.Int32
	if err := a.Documents.Boot(t.Context(), documents.InstanceOptions{
		Check: time.Hour, MinBackoff: time.Hour, MaxBackoff: time.Hour, OnLost: func() { lost.Add(1) },
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Documents.Close)
	client := lostClient(t, a)
	if err := lostCommit(t, client, 1); err != nil {
		t.Fatal(err)
	}
	terminateHolder(t, pool)
	takeLock(t, url)
	if err := lostCommit(t, client, 2); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("commit while another session holds the lock: %v (lost callbacks %d)", err, lost.Load())
	}
}

// A process that boots while another holds the lock admits no stream: the Write handshake
// fails UNAVAILABLE (review streams-admitted-before-first-lock).
func TestDocumentsBootWithoutLock(t *testing.T) {
	a, _, url := lostApp(t)
	takeLock(t, url)
	if err := a.Documents.Boot(t.Context(), documents.InstanceOptions{BootWait: 100 * time.Millisecond,
		Check: 50 * time.Millisecond, MinBackoff: 50 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Documents.Close)
	client := lostClient(t, a)
	w := client.Write(t.Context())
	w.RequestHeader().Set("Authorization", "Bearer owner")
	t.Cleanup(func() { _ = w.CloseRequest(); _ = w.CloseResponse() })
	if err := w.Send(&pb.WriteRequest{Database: lostDatabase}); err != nil {
		t.Fatal(err)
	}
	if r, err := w.Receive(); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Write handshake while another instance holds the lock: %v, %v", r, err)
	}
}
