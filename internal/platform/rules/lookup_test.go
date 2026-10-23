package rules

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// lookupRules is a Firestore file whose /c/{id} grants get when cond holds.
func lookupRules(t *testing.T, cond expr) *Ruleset {
	t.Helper()
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{allowb("get", cond)}})}})
	return compile(t, Firestore, rulesFile("2", Firestore, body))
}

// lookupRequest is a get of c/d whose request and resource variables hold paths of documents.
func lookupRequest(docs Documents, request, resource string) *Request {
	req := requestFor(docs)
	req.Variables["request"] = docValue(strings.Split(request, "/")...)
	req.Variables["resource"] = docValue(strings.Split(resource, "/")...)
	return req
}

func requestFor(docs Documents) *Request { return request(MethodGet, docPath("c", "d"), docs) }

func TestLookups(t *testing.T) {
	stored := map[string]Value{"databases/(default)/documents/x/1": Map(map[string]Value{"a": Bool(true)})}
	get := func(v string) expr { return call("get", ident(v)) }
	exists := func(v string) expr { return call("exists", ident(v)) }
	for _, c := range []struct {
		name  string
		cond  expr
		want  Outcome
		reads int
	}{
		{"exists of a stored document", exists("request"), OutcomeTrue, 1},
		{"exists of a missing one", exists("resource"), OutcomeFalse, 1},
		{"get of a stored document is its value", or(get("request"), tru()), OutcomeTrue, 1},
		{"get of a missing one fails", get("resource"), OutcomeError, 1},
		{"get of a missing one, absorbed", or(get("resource"), tru()), OutcomeTrue, 1},
		{"get and exists of one path: one read", and(exists("request"), or(get("request"), tru())), OutcomeTrue, 1},
		{"getAfter is another state", and(exists("request"), call("existsAfter", ident("request"))), OutcomeTrue, 2},
		{"a path that is not a document", call("exists", ident("id")), OutcomeError, 0},
		{"a collection", call("exists", lit(docValue("x"))), OutcomeError, 0},
		{"another database", call("exists", lit(PathOf([]string{"databases", "other", "documents", "x", "1"}))), OutcomeError, 0},
		{"no argument", call("exists"), OutcomeError, 0},
		{"two arguments", call("exists", ident("request"), ident("request")), OutcomeError, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := &memDocs{docs: stored}
			got := evaluate(t, lookupRules(t, c.cond), lookupRequest(docs, "x/1", "x/2"))
			if len(got.Statements) != 1 || got.Statements[0].Outcome != c.want {
				t.Errorf("%+v, want %v", got.Statements, c.want)
			}
			if docs.total() != c.reads {
				t.Errorf("%d reads (%v), want %d", docs.total(), docs.reads, c.reads)
			}
			for _, p := range docs.projects {
				if p != 7 {
					t.Errorf("a read for project %d, not the request's", p)
				}
			}
		})
	}
}

// Firestore's limits: 10 lookups an operation and 20 a request; a cached lookup does not count;
// going past one denies the whole evaluation, which || does not absorb (ADR-021 §4).
func TestLookupBudget(t *testing.T) {
	cond := or(or(call("exists", ident("request")), call("exists", ident("resource"))), tru())
	rs := lookupRules(t, cond)
	rs.lim.LookupsPerOperation = 1
	docs := &memDocs{}
	d := evaluate(t, rs, lookupRequest(docs, "x/1", "x/2"))
	if d.Allow || !strings.Contains(d.Statements[0].Message, "limit of 1 document lookups") || docs.total() != 1 {
		t.Fatalf("past the operation's limit: %+v after %d reads", d, docs.total())
	}
	// The same path twice is one lookup.
	if d := evaluate(t, rs, lookupRequest(docs, "x/1", "x/1")); !d.Allow {
		t.Fatalf("a cached lookup counted: %+v", d)
	}

	// The request's limit is shared by the operations of a batch, and so is the cache.
	rs = lookupRules(t, cond)
	rs.lim.LookupsPerRequest = 3
	budget := NewBudget()
	docs = &memDocs{}
	for i, c := range []struct {
		request, resource string
		allow             bool
	}{
		{"x/1", "x/2", true},  // two lookups
		{"x/1", "x/2", true},  // both cached
		{"x/2", "x/3", true},  // one more: three
		{"x/3", "x/4", false}, // the fourth
	} {
		req := lookupRequest(docs, c.request, c.resource)
		req.Budget = budget
		if d := evaluate(t, rs, req); d.Allow != c.allow {
			t.Fatalf("operation %d: %+v", i+1, d)
		}
	}
	if docs.total() != 3 {
		t.Errorf("%d reads, want 3", docs.total())
	}
}

// Storage reads Firestore through firestore.get and firestore.exists, at most two documents in
// an evaluation (the documentation, and production: Pyric's lookup budget). Until v0.4 its
// Documents is NoDocuments: every lookup is an error that || may absorb.
func TestStorageLookups(t *testing.T) {
	ex := func(v string) expr { return nscall("firestore", "exists", ident(v)) }
	body := block{matches: []*match{matchb("/{all=**}", block{allows: []*allow{
		allowb("read", and(and(ex("request"), ex("resource")), ex("all"))),
		allowb("create", or(ex("request"), tru())),
		allowb("update", ex("request")),
		allowb("delete", or(call("get", ident("request")), tru())),
	}})}}
	rs := compile(t, Storage, rulesFile("2", Storage, body))
	stored := map[string]Value{}
	for _, p := range []string{"x/1", "x/2", "c/d"} {
		stored["databases/(default)/documents/"+p] = Map(nil)
	}
	docs := &memDocs{docs: stored}
	req := lookupRequest(docs, "x/1", "x/2")
	d := evaluate(t, rs, req)
	if d.Allow || !strings.Contains(d.Statements[0].Message, "limit of 2") || docs.total() != 2 {
		t.Fatalf("a third document: %+v after %d reads", d, docs.total())
	}
	req = lookupRequest(docs, "x/1", "x/1")
	if d := evaluate(t, rs, req); !d.Allow {
		t.Fatalf("two documents, one twice: %+v", d)
	}

	req = lookupRequest(NoDocuments{}, "x/1", "x/2")
	req.Method = MethodCreate
	if d := evaluate(t, rs, req); !d.Allow || d.Statements[0].Outcome != OutcomeTrue {
		t.Fatalf("NoDocuments not absorbed by ||: %+v", d)
	}
	req = lookupRequest(NoDocuments{}, "x/1", "x/2")
	req.Method = MethodUpdate
	d = evaluate(t, rs, req)
	if d.Allow || d.Statements[0].Message != ErrNoDocuments.Error() {
		t.Fatalf("NoDocuments: %+v", d)
	}
	// get() is Firestore's: in Storage it is a function that does not exist.
	req = lookupRequest(NoDocuments{}, "x/1", "x/2")
	req.Method = MethodDelete
	if d := evaluate(t, rs, req); !d.Allow {
		t.Fatalf("an unknown function not absorbed: %+v", d)
	}
}

// A Documents that fails otherwise is an outage: Evaluate returns its error, never a decision.
func TestLookupOutage(t *testing.T) {
	broken := errors.New("connection refused")
	rs := lookupRules(t, or(call("exists", ident("request")), tru()))
	d, err := rs.Evaluate(context.Background(), lookupRequest(&memDocs{err: broken}, "x/1", "x/2"))
	if !errors.Is(err, broken) || d.Allow {
		t.Fatalf("Evaluate = %+v, %v; want the outage", d, err)
	}
	// A failed lookup is not cached: the next one asks again.
	docs := &memDocs{err: ErrNoDocuments}
	req := lookupRequest(docs, "x/1", "x/2")
	rs = lookupRules(t, or(call("exists", ident("request")), call("exists", ident("request"))))
	if d := evaluate(t, rs, req); d.Allow || docs.total() != 2 {
		t.Fatalf("%+v after %d reads", d, docs.total())
	}
}

func TestEncodePath(t *testing.T) {
	if encodePath([]string{"a/b", "c"}) == encodePath([]string{"a", "b/c"}) ||
		encodePath([]string{"1:a"}) == encodePath([]string{"1", "a"}) {
		t.Error("two paths share a key")
	}
}
