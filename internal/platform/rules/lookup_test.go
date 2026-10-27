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
		{"get of a missing one is an error", get("resource"), OutcomeError, 1},
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

// Firestore's limits: 10 distinct documents an operation and 20 a request. A document looked up
// again is read once, counts once against the request, and against each operation that looks it
// up; a document the request writes is free; a pooled budget has the request's limit alone.
// Going past a limit is an evaluation error, which || absorbs (production, f5-rules-lookup-limits
// miss-exists; ADR-021 §4).
func TestLookupBudget(t *testing.T) {
	stored := map[string]Value{}
	for _, p := range []string{"x/1", "x/2", "x/3", "x/4"} {
		stored["databases/(default)/documents/"+p] = Map(nil)
	}
	both := and(call("exists", ident("request")), call("exists", ident("resource")))
	limited := func(cond expr, perOperation, perRequest int) *Ruleset {
		rs := lookupRules(t, cond)
		rs.lim.LookupsPerOperation, rs.lim.LookupsPerRequest = perOperation, perRequest
		return rs
	}
	docs := &memDocs{docs: stored}
	d := evaluate(t, limited(both, 1, 20), lookupRequest(docs, "x/1", "x/2"))
	if d.Allow || !strings.Contains(d.Statements[0].Message, "limit of 1 document lookups") || docs.total() != 1 {
		t.Fatalf("past the operation's limit: %+v after %d reads", d, docs.total())
	}
	if d := evaluate(t, limited(or(both, tru()), 1, 20), lookupRequest(docs, "x/1", "x/2")); !d.Allow {
		t.Fatalf("past the operation's limit, not absorbed by ||: %+v", d)
	}
	// The same path twice is one document.
	if d := evaluate(t, limited(both, 1, 20), lookupRequest(docs, "x/1", "x/1")); !d.Allow {
		t.Fatalf("one document counted twice: %+v", d)
	}
	// A pooled budget has the request's limit alone; an exempt document counts against none.
	req := lookupRequest(docs, "x/1", "x/2")
	req.Budget = NewPooledBudget()
	if d := evaluate(t, limited(both, 1, 2), req); !d.Allow {
		t.Fatalf("a pooled budget held to the operation's limit: %+v", d)
	}
	req = lookupRequest(docs, "x/1", "x/2")
	req.Budget.Exempt([]string{"databases", "(default)", "documents", "x", "2"})
	if d := evaluate(t, limited(both, 1, 1), req); !d.Allow {
		t.Fatalf("an exempt document counted: %+v", d)
	}

	// The request's limit is shared by the operations of a batch, and so is the cache; each
	// operation counts a document another read first against its own limit.
	budget := NewBudget()
	docs = &memDocs{docs: stored}
	for i, c := range []struct {
		request, resource string
		perOperation      int
		allow             bool
	}{
		{"x/1", "x/2", 2, true},  // two documents
		{"x/1", "x/2", 2, true},  // both cached
		{"x/1", "x/2", 1, false}, // cached, but two for this operation
		{"x/2", "x/3", 2, true},  // one more: three
		{"x/3", "x/4", 2, false}, // the fourth
	} {
		req := lookupRequest(docs, c.request, c.resource)
		req.Budget = budget
		if d := evaluate(t, limited(both, c.perOperation, 3), req); d.Allow != c.allow {
			t.Fatalf("operation %d: %+v", i+1, d)
		}
	}
	if docs.total() != 3 {
		t.Errorf("%d reads, want 3", docs.total())
	}

	// A read budget counts against each operation only the documents new to the request
	// (f5-rules-lookups.json limit.batch-get-g11x2 denied, lookup-limits batch-get-r1a-r20a
	// allowed), and against the request each document once.
	budget = NewReadBudget()
	for i, c := range []struct {
		request, resource string
		allow             bool
	}{
		{"x/1", "x/2", false}, // two new documents, past the operation's 1
		{"x/1", "x/1", true},  // counted by the first: free
		{"x/1", "x/2", true},  // both counted already
		{"x/3", "x/2", true},  // one new: three for the request
		{"x/4", "x/4", false}, // the fourth, past the request's 3
	} {
		req := lookupRequest(docs, c.request, c.resource)
		req.Budget = budget
		if d := evaluate(t, limited(both, 1, 3), req); d.Allow != c.allow {
			t.Fatalf("read operation %d: %+v", i+1, d)
		}
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
	// A lookup Firestore cannot answer is cached with its error: repeating it is free.
	docs := &memDocs{err: ErrNoDocuments}
	req := lookupRequest(docs, "x/1", "x/2")
	rs = lookupRules(t, or(call("exists", ident("request")), call("exists", ident("request"))))
	if d := evaluate(t, rs, req); d.Allow || docs.total() != 1 {
		t.Fatalf("%+v after %d reads", d, docs.total())
	}
}

// get() of a missing document is an evaluation error, not null as the reference says
// (production: f5-rules-lookups.json read.lk-get-missing, read.lk-get-missing-or; ADR-021 F5-A2b).
func TestGetMissingIsAnError(t *testing.T) {
	got := evaluate(t, lookupRules(t, call("get", ident("resource"))), lookupRequest(&memDocs{}, "x/1", "x/2"))
	if got.Statements[0].Outcome != OutcomeError || !strings.Contains(got.Statements[0].Message, "does not exist") {
		t.Errorf("get of a missing document: %+v", got.Statements[0])
	}
}

// In v0.3 every Storage lookup fails; repeating one is free, so its errors stay absorbable
// however often a rule names the same document (deployed Storage counts a repeated document
// once: Pyric's lookup-budget "repeat").
func TestRepeatedFailedLookup(t *testing.T) {
	ex := func() expr { return nscall("firestore", "exists", ident("request")) }
	body := block{matches: []*match{matchb("/{all=**}", block{allows: []*allow{
		allowb("read", or(or(or(ex(), ex()), ex()), tru())),
	}})}}
	rs := compile(t, Storage, rulesFile("2", Storage, body))
	if d := evaluate(t, rs, lookupRequest(NoDocuments{}, "u/1", "u/2")); !d.Allow {
		t.Fatalf("three references to one document denied: %+v", d)
	}
}

func TestEncodePath(t *testing.T) {
	if encodePath([]string{"a/b", "c"}) == encodePath([]string{"a", "b/c"}) ||
		encodePath([]string{"1:a"}) == encodePath([]string{"1", "a"}) {
		t.Error("two paths share a key")
	}
}
