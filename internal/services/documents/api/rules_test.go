package api_test

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The security rules' binding (api/rules.go): methods, variables, lookups and their limits, and
// who bypasses them.

// lookupList is a rules list of n exists() lookups of <prefix><i> under open, which evaluates
// them all.
func lookupList(prefix string, n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, fmt.Sprintf("exists(/databases/$(database)/documents/open/%s%d)", prefix, i))
	}
	return "[" + strings.Join(parts, ", ") + "].size() == " + fmt.Sprint(n)
}

var bindingRules = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /open/{id} {
      allow read, write: if true;
    }
    match /owned/{uid} {
      allow get: if request.auth != null && request.auth.uid == uid && request.method == 'get'
                 && request.resource == null;
      allow create: if request.auth.uid == uid && resource == null && request.method == 'create'
                    && request.resource.data.owner == uid && request.resource.id == uid
                    && request.resource.__name__ == /databases/$(database)/documents/owned/$(uid)
                    && request.path == /databases/$(database)/documents/owned/$(uid);
      allow update: if request.auth.uid == uid && request.method == 'update'
                    && resource.data.owner == uid && request.resource.data.n > resource.data.n;
      allow delete: if request.auth.uid == uid && request.method == 'delete'
                    && resource.data.owner == uid && request.resource == null;
    }
    match /claims/{id} {
      allow get: if request.auth.token.role == 'admin' && request.auth.token.firebase.sign_in_provider == 'password';
    }
    match /stamped/{id} {
      allow create: if request.resource.data.ts == request.time;
    }
    match /lookup/{id} {
      allow get: if get(/databases/$(database)/documents/open/$(id)).data.ok == true;
      allow create: if getAfter(/databases/$(database)/documents/open/$(id)).data.ok == true
                    && !exists(/databases/$(database)/documents/open/$(id));
      allow delete: if !existsAfter(/databases/$(database)/documents/lookup/$(id));
    }
    match /ten/{id} {
      allow get: if ` + lookupList("t", 10) + `;
    }
    match /eleven/{id} {
      allow get: if ` + lookupList("e", 11) + `;
    }
    match /eight/{id} {
      allow get: if ` + lookupList("$(id)/sub/x", 8) + `;
    }
    match /shared/{id} {
      allow get: if ` + lookupList("s", 8) + `;
    }
  }
}`

type rulesEnv struct {
	env *apitest.Env
	db  core.DB
}

func newRulesEnv(t *testing.T, o apitest.Options) *rulesEnv {
	t.Helper()
	env := apitest.New(t, o)
	r := &rulesEnv{env: env, db: core.DB{Project: env.Number, ProjectID: apitest.Project, ID: core.DefaultDatabase}}
	if _, err := env.Store.Database(t.Context(), r.db); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rulesEnv) name(path string) string { return docsRoot + "/" + path }

// seed writes documents as an admin, past the rules.
func (r *rulesEnv) seed(t *testing.T, path string, fields map[string]value.Value) {
	t.Helper()
	if _, err := r.env.Store.Commit(t.Context(), r.db, []core.Write{{Kind: core.WriteUpdate,
		Path: core.Path(strings.Split(path, "/")), Fields: value.MapOf(fields)}}); err != nil {
		t.Fatal(err)
	}
}

func call(token string) api.Call {
	c := api.Call{Transport: authn.Transport{Peer: netip.MustParseAddr("192.0.2.7")}}
	if token != "" {
		c.Authorization = "Bearer " + token
	}
	return c
}

func update(name string, fields map[string]*pb.Value) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: name, Fields: fields}}}
}

func del(name string) *pb.Write { return &pb.Write{Operation: &pb.Write_Delete{Delete: name}} }

func str(s string) *pb.Value { return &pb.Value{ValueType: &pb.Value_StringValue{StringValue: s}} }
func num(n int64) *pb.Value  { return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}} }
func yes() *pb.Value         { return &pb.Value{ValueType: &pb.Value_BooleanValue{BooleanValue: true}} }
func codeOf(err error) wire.Code {
	if we, ok := errors.AsType[*wire.Error](err); ok {
		return we.Code
	}
	if err != nil {
		return wire.CodeUnknown
	}
	return wire.CodeOK
}

func TestRulesBinding(t *testing.T) {
	r := newRulesEnv(t, apitest.Options{})
	r.env.PublishFirestoreRules(t, bindingRules)
	alice := apitest.UserToken(t, "alice", nil)
	admin := apitest.UserToken(t, "carol", map[string]any{"role": "admin"})
	r.seed(t, "owned/alice", map[string]value.Value{"owner": value.String("alice"), "n": value.Int(1)})
	r.seed(t, "owned/bob", map[string]value.Value{"owner": value.String("bob"), "n": value.Int(1)})
	r.seed(t, "claims/x", map[string]value.Value{})
	r.seed(t, "open/ok", map[string]value.Value{"ok": value.Bool(true)})
	r.seed(t, "lookup/ok", map[string]value.Value{})
	r.seed(t, "lookup/no", map[string]value.Value{})
	s := r.env.Service
	ctx := t.Context()

	get := func(token, path string) error {
		_, err := s.GetDocument(ctx, call(token), &pb.GetDocumentRequest{Name: r.name(path)})
		return err
	}
	commit := func(token string, ws ...*pb.Write) error {
		_, err := s.Commit(ctx, call(token), &pb.CommitRequest{Database: r.db.Name(), Writes: ws})
		return err
	}
	for _, c := range []struct {
		name string
		do   func() error
		want wire.Code
	}{
		{"get own", func() error { return get(alice, "owned/alice") }, wire.CodeOK},
		{"get another's", func() error { return get(alice, "owned/bob") }, wire.CodePermissionDenied},
		{"get unauthenticated", func() error { return get("", "owned/alice") }, wire.CodePermissionDenied},
		{"get missing allowed is NOT_FOUND", func() error { return get("", "open/none") }, wire.CodeNotFound},
		{"get missing denied is PERMISSION_DENIED", func() error { return get(alice, "owned/bob2") }, wire.CodePermissionDenied},
		{"get by claim", func() error { return get(admin, "claims/x") }, wire.CodeOK},
		{"get without claim", func() error { return get(alice, "claims/x") }, wire.CodePermissionDenied},
		{"unmatched path", func() error { return get(alice, "elsewhere/x") }, wire.CodePermissionDenied},
		{"create own", func() error {
			return commit(alice, update(r.name("owned/alice2"), map[string]*pb.Value{"owner": str("alice")}))
		}, wire.CodePermissionDenied}, // uid != alice2
		{"create matching", func() error {
			return commit(apitest.UserToken(t, "dave", nil), update(r.name("owned/dave"), map[string]*pb.Value{"owner": str("dave")}))
		}, wire.CodeOK},
		{"create with wrong data", func() error {
			return commit(apitest.UserToken(t, "erin", nil), update(r.name("owned/erin"), map[string]*pb.Value{"owner": str("x")}))
		}, wire.CodePermissionDenied},
		{"update up", func() error {
			return commit(alice, update(r.name("owned/alice"), map[string]*pb.Value{"owner": str("alice"), "n": num(2)}))
		}, wire.CodeOK},
		{"update down", func() error {
			return commit(alice, update(r.name("owned/alice"), map[string]*pb.Value{"owner": str("alice"), "n": num(0)}))
		}, wire.CodePermissionDenied},
		{"update another's", func() error {
			return commit(alice, update(r.name("owned/bob"), map[string]*pb.Value{"owner": str("bob"), "n": num(9)}))
		}, wire.CodePermissionDenied},
		{"batch denied whole", func() error {
			return commit(alice, update(r.name("open/b1"), nil), update(r.name("owned/bob"), map[string]*pb.Value{"n": num(9)}))
		}, wire.CodePermissionDenied},
		{"request.time is the server timestamp", func() error {
			w := update(r.name("stamped/a"), nil)
			w.UpdateTransforms = []*pb.DocumentTransform_FieldTransform{{FieldPath: "ts",
				TransformType: &pb.DocumentTransform_FieldTransform_SetToServerValue{SetToServerValue: pb.DocumentTransform_FieldTransform_REQUEST_TIME}}}
			return commit("", w)
		}, wire.CodeOK},
		{"get() reads", func() error { return get("", "lookup/ok") }, wire.CodeOK},
		{"get() of a missing document is null", func() error { return get("", "lookup/no") }, wire.CodePermissionDenied},
		{"getAfter() sees the batch", func() error {
			return commit("", update(r.name("open/z"), map[string]*pb.Value{"ok": yes()}), update(r.name("lookup/z"), nil))
		}, wire.CodeOK},
		{"getAfter() without the batch", func() error { return commit("", update(r.name("lookup/y"), nil)) }, wire.CodePermissionDenied},
		{"existsAfter() of the deleted document", func() error { return commit("", del(r.name("lookup/ok"))) }, wire.CodeOK},
		{"ten lookups", func() error { return get("", "ten/a") }, wire.CodeNotFound},
		{"eleven lookups", func() error { return get("", "eleven/a") }, wire.CodePermissionDenied},
		{"delete own", func() error { return commit(alice, del(r.name("owned/alice"))) }, wire.CodeOK},
		{"delete another's", func() error { return commit(alice, del(r.name("owned/bob"))) }, wire.CodePermissionDenied},
	} {
		if got := codeOf(c.do()); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// A denied create of a document that exists says nothing of its existence.
	_, err := s.CreateDocument(ctx, call(alice), &pb.CreateDocumentRequest{Parent: docsRoot, CollectionId: "owned", DocumentId: "bob"})
	if codeOf(err) != wire.CodePermissionDenied {
		t.Errorf("CreateDocument of an existing document, denied: %v", err)
	}
	if _, err := s.CreateDocument(ctx, call(""), &pb.CreateDocumentRequest{Parent: docsRoot, CollectionId: "open", DocumentId: "ok"}); codeOf(err) != wire.CodeAlreadyExists {
		t.Errorf("CreateDocument of an existing document, allowed: %v", err)
	}
	if _, err := s.DeleteDocument(ctx, call(alice), &pb.DeleteDocumentRequest{Name: r.name("owned/bob")}); codeOf(err) != wire.CodePermissionDenied {
		t.Errorf("DeleteDocument of another's: %v", err)
	}
	if _, err := s.UpdateDocument(ctx, call(""), &pb.UpdateDocumentRequest{Document: &pb.Document{Name: r.name("open/u")}}); err != nil {
		t.Errorf("UpdateDocument allowed: %v", err)
	}
}

// One budget per request: 20 lookups, cached ones free; BatchGet's documents share it.
func TestRulesLookupBudget(t *testing.T) {
	r := newRulesEnv(t, apitest.Options{})
	r.env.PublishFirestoreRules(t, bindingRules)
	batch := func(paths ...string) error {
		var names []string
		for _, p := range paths {
			names = append(names, r.name(p))
		}
		return r.env.Service.BatchGetDocuments(t.Context(), call(""), &pb.BatchGetDocumentsRequest{Database: r.db.Name(), Documents: names},
			func(*pb.BatchGetDocumentsResponse) error { return nil })
	}
	for _, c := range []struct {
		name  string
		paths []string
		want  wire.Code
	}{
		{"16 lookups", []string{"eight/a", "eight/b"}, wire.CodeOK},
		{"24 lookups", []string{"eight/a", "eight/b", "eight/c"}, wire.CodePermissionDenied},
		{"8 lookups, cached for the rest", []string{"shared/a", "shared/b", "shared/c", "shared/d"}, wire.CodeOK},
		{"one denied, all denied", []string{"open/a", "owned/x"}, wire.CodePermissionDenied},
	} {
		if got := codeOf(batch(c.paths...)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Who answers to the rules: admins and dev_open_rules on loopback bypass them; anyone else does,
// with the locked default when no release is published; a token that is not valid is refused.
func TestRulesCallers(t *testing.T) {
	r := newRulesEnv(t, apitest.Options{DevOpenRules: true})
	r.seed(t, "c/d", map[string]value.Value{})
	s := r.env.Service
	get := func(c api.Call) error {
		_, err := s.GetDocument(t.Context(), c, &pb.GetDocumentRequest{Name: r.name("c/d")})
		return err
	}
	loopback := func(c api.Call) api.Call { c.Transport.Peer = netip.MustParseAddr("127.0.0.1"); return c }
	user := apitest.UserToken(t, "u", nil)
	expired := apitest.UserToken(t, "u", map[string]any{"exp": 1})
	forged := strings.TrimSuffix(user, "c2ln") + "eA"
	for _, c := range []struct {
		name string
		call api.Call
		want wire.Code
	}{
		{"admin", loopback(call(r.env.JWT(t))), wire.CodeOK},
		{"unauthenticated, locked default", call(""), wire.CodePermissionDenied},
		{"end user, locked default", call(user), wire.CodePermissionDenied},
		{"end user, dev_open_rules on loopback", loopback(call(user)), wire.CodeOK},
		{"unauthenticated, dev_open_rules on loopback", loopback(call("")), wire.CodeOK},
		{"expired token", call(expired), wire.CodeUnauthenticated},
		{"forged token", call(forged), wire.CodeUnauthenticated},
		{"forged token on loopback", loopback(call(forged)), wire.CodeUnauthenticated},
	} {
		if got := codeOf(get(c.call)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Lists are bound (rules_list.go); ListCollectionIds stays an admin's.
	r.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore { match /databases/{d}/documents { match /{p=**} { allow read, write: if true; } } }`)
	if err := get(call(user)); err != nil {
		t.Fatalf("allowed get: %v", err)
	}
	q := &pb.RunQueryRequest{Parent: docsRoot, QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: &pb.StructuredQuery{
		From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "c"}}}}}
	if err := s.RunQuery(t.Context(), call(user), q, func(*pb.RunQueryResponse) error { return nil }); err != nil {
		t.Errorf("RunQuery of an end user: %v", err)
	}
	if _, err := s.ListDocuments(t.Context(), call(user), &pb.ListDocumentsRequest{Parent: docsRoot, CollectionId: "c"}); err != nil {
		t.Errorf("ListDocuments of an end user: %v", err)
	}
	if _, err := s.ListCollectionIds(t.Context(), call(user), &pb.ListCollectionIdsRequest{Parent: docsRoot}); codeOf(err) != wire.CodePermissionDenied {
		t.Errorf("ListCollectionIds of an end user: %v", err)
	}
	if err := s.RunQuery(t.Context(), loopback(call(r.env.JWT(t))), q, func(*pb.RunQueryResponse) error { return nil }); err != nil {
		t.Errorf("RunQuery of an admin: %v", err)
	}
}

// The rules hold in transactions, on the Write stream and per write in BatchWrite; an end user's
// transaction is bound to the uid verified.
func TestRulesTransactionsAndStreams(t *testing.T) {
	r := newRulesEnv(t, apitest.Options{})
	r.env.PublishFirestoreRules(t, bindingRules)
	r.seed(t, "owned/alice", map[string]value.Value{"owner": value.String("alice"), "n": value.Int(1)})
	r.seed(t, "owned/bob", map[string]value.Value{"owner": value.String("bob"), "n": value.Int(1)})
	s, ctx := r.env.Service, t.Context()
	alice := apitest.UserToken(t, "alice", nil)

	begin := func(token string) []byte {
		res, err := s.BeginTransaction(ctx, call(token), &pb.BeginTransactionRequest{Database: r.db.Name(),
			Options: &pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{
				ConcurrencyMode: pb.TransactionOptions_OPTIMISTIC}}}})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetTransaction()
	}
	txn := begin(alice)
	if _, err := s.GetDocument(ctx, call(alice), &pb.GetDocumentRequest{Name: r.name("owned/alice"),
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: txn}}); err != nil {
		t.Fatalf("read in a transaction: %v", err)
	}
	if _, err := s.GetDocument(ctx, call(alice), &pb.GetDocumentRequest{Name: r.name("owned/bob"),
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: txn}}); codeOf(err) != wire.CodePermissionDenied {
		t.Fatalf("denied read in a transaction: %v", err)
	}
	// Another user cannot use alice's transaction: it is bound to the uid verified.
	if _, err := s.Commit(ctx, call(apitest.UserToken(t, "mallory", nil)), &pb.CommitRequest{Database: r.db.Name(), Transaction: txn}); codeOf(err) == wire.CodeOK {
		t.Fatal("another user committed alice's transaction")
	}
	txn = begin(alice)
	_, err := s.Commit(ctx, call(alice), &pb.CommitRequest{Database: r.db.Name(), Transaction: txn,
		Writes: []*pb.Write{update(r.name("owned/bob"), map[string]*pb.Value{"owner": str("bob"), "n": num(5)})}})
	if codeOf(err) != wire.CodePermissionDenied {
		t.Fatalf("denied transaction commit: %v", err)
	}
	txn = begin(alice)
	if _, err := s.Commit(ctx, call(alice), &pb.CommitRequest{Database: r.db.Name(), Transaction: txn,
		Writes: []*pb.Write{update(r.name("owned/alice"), map[string]*pb.Value{"owner": str("alice"), "n": num(5)})}}); err != nil {
		t.Fatalf("allowed transaction commit: %v", err)
	}

	res, err := s.BatchWrite(ctx, call(alice), &pb.BatchWriteRequest{Database: r.db.Name(), Writes: []*pb.Write{
		update(r.name("open/w"), nil), update(r.name("owned/bob"), map[string]*pb.Value{"n": num(9)})}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status[0].GetCode() != 0 || wire.Code(res.Status[1].GetCode()) != wire.CodePermissionDenied {
		t.Errorf("BatchWrite statuses: %v", res.Status)
	}

	w, h, err := s.OpenWriteStream(ctx, call(alice), &pb.WriteRequest{Database: r.db.Name()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, &pb.WriteRequest{StreamToken: h.GetStreamToken(), Writes: []*pb.Write{
		update(r.name("owned/alice"), map[string]*pb.Value{"owner": str("alice"), "n": num(6)})}}); err != nil {
		t.Fatalf("allowed stream write: %v", err)
	}
	if _, err := w.Write(ctx, &pb.WriteRequest{StreamToken: h.GetStreamToken(), Writes: []*pb.Write{
		update(r.name("owned/bob"), map[string]*pb.Value{"owner": str("bob"), "n": num(6)})}}); codeOf(err) != wire.CodePermissionDenied {
		t.Fatalf("denied stream write: %v", err)
	}
}
