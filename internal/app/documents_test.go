package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// The Document Database wired end to end on Postgres: Boot takes the single-instance lock and
// gives an existing project its '(default)', and the entry point serves gRPC over h2c and REST
// with the dev owner token of a loopback server.
func TestDocumentsWiring(t *testing.T) {
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
	proj, err := a.Service.CreateProject(t.Context(), owner, core.NewProject{ID: "wired-app", DisplayName: "wired"})
	if err != nil {
		t.Fatal(err)
	}

	// Before Boot the store is not open: the Document Database is not served.
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
	name := "projects/wired-app/databases/(default)/documents/c/d"
	get := func() error {
		r := connect.NewRequest(&pb.GetDocumentRequest{Name: name})
		r.Header().Set("Authorization", "Bearer owner")
		_, err := client.GetDocument(t.Context(), r)
		return err
	}
	if ce, ok := get().(*connect.Error); !ok || ce.Code() != connect.CodeUnavailable || !strings.Contains(ce.Message(), "did not call Boot") {
		t.Errorf("before Boot: %v", ce)
	}

	if err := a.Documents.Boot(t.Context(), documents.InstanceOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Documents.Close)
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM documents.databases WHERE project_number = $1 AND database_id = '(default)'`, proj.Number).Scan(&n); err != nil || n != 1 {
		t.Errorf("Boot gave the project %d '(default)' databases (%v)", n, err)
	}
	err = get()
	if ce, ok := err.(*connect.Error); !ok || ce.Code() != connect.CodeNotFound || ce.Message() != `Document "`+name+`" not found.` {
		t.Errorf("gRPC GetDocument: %v", err)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, ts.URL+"/v1/"+name, strings.NewReader(`{"fields":{"a":{"integerValue":"1"}}}`))
	req.Header.Set("Authorization", "Bearer owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"name":"`+name+`"`) {
		t.Errorf("REST PATCH: %d %s", resp.StatusCode, body)
	}
	if err := get(); err != nil {
		t.Errorf("gRPC GetDocument after the REST write: %v", err)
	}

	// The firebaserules API is served with Storage off, for firebase deploy --only
	// firestore:rules: a request without a credential is refused, not an unknown route.
	resp, err = http.Get(ts.URL + "/v1/projects/wired-app/releases")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("firebaserules without Storage: %d, want 401", resp.StatusCode)
	}

	// Storage's rules read the document through what app gives Storage (ADR-021 §3): of the
	// request's project, never of another.
	rs, issues, err := a.Service.CompileRules(t.Context(), rules.Storage, rules.File{Name: "storage.rules", Content: `rules_version = '2';
service firebase.storage { match /b/{bucket}/o { match /{f} {
  allow read: if firestore.get(/databases/(default)/documents/c/d).data.a == 1;
} } }`})
	if err != nil || rs == nil {
		t.Fatalf("CompileRules: %v %v", err, issues.Err())
	}
	storageRead := func(project int64) bool {
		d, err := rs.Evaluate(t.Context(), &rules.Request{
			Project: project, Method: rules.MethodGet, Path: rules.Literals("b", "bucket", "o", "f"),
			Variables: map[string]rules.Value{"request": rules.Map(map[string]rules.Value{"auth": rules.Null()}), "resource": rules.Null()},
			Documents: a.Documents.RuleDocuments(), Budget: rules.NewBudget(), Inflight: a.RulesInflight,
		})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		return d.Allow
	}
	other, err := a.Service.CreateProject(t.Context(), owner, core.NewProject{ID: "other-app", DisplayName: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if !storageRead(proj.Number) || storageRead(other.Number) {
		t.Errorf("firestore.get() from Storage's rules: own project %v, another %v, want true, false",
			storageRead(proj.Number), storageRead(other.Number))
	}

	// Transactions are served: the dev owner's is an admin's, committed once.
	br := connect.NewRequest(&pb.BeginTransactionRequest{Database: "projects/wired-app/databases/(default)"})
	br.Header().Set("Authorization", "Bearer owner")
	began, err := client.BeginTransaction(t.Context(), br)
	if err != nil {
		t.Fatal(err)
	}
	commit := func() error {
		r := connect.NewRequest(&pb.CommitRequest{Database: "projects/wired-app/databases/(default)", Transaction: began.Msg.Transaction,
			Writes: []*pb.Write{{Operation: &pb.Write_Delete{Delete: name}}}})
		r.Header().Set("Authorization", "Bearer owner")
		_, err := client.Commit(t.Context(), r)
		return err
	}
	if err := commit(); err != nil {
		t.Errorf("commit in a transaction: %v", err)
	}
	if ce, ok := commit().(*connect.Error); !ok || ce.Code() != connect.CodeAborted {
		t.Errorf("second commit in the transaction: %v", ce)
	}

	// A project whose deletion has expired loses its Document Database rows (review M5).
	if _, err := a.Service.DeleteProject(t.Context(), owner, proj.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE control.projects SET expire_time = now() - interval '1 second' WHERE project_number = $1`, proj.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Service.PurgeExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM documents.documents WHERE project_number = $1)
		+ (SELECT count(*) FROM documents.changelog WHERE project_number = $1)
		+ (SELECT count(*) FROM documents.databases WHERE project_number = $1)`, proj.Number).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d Document Database rows left after the purge (%v)", n, err)
	}
}
