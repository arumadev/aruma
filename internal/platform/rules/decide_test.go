package rules

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// read covers get and list, write covers create, update and delete, and each name itself.
func TestMethodCoverage(t *testing.T) {
	for _, c := range []struct {
		methods string
		grants  []Method
	}{
		{"read", []Method{MethodGet, MethodList}},
		{"write", []Method{MethodCreate, MethodUpdate, MethodDelete}},
		{"get", []Method{MethodGet}},
		{"list", []Method{MethodList}},
		{"create", []Method{MethodCreate}},
		{"update", []Method{MethodUpdate}},
		{"delete", []Method{MethodDelete}},
		{"get, update", []Method{MethodGet, MethodUpdate}},
		{"foo", nil},
	} {
		body := block{matches: []*match{matchb("/b/{bucket}/o/{name=**}", block{allows: []*allow{allowb(c.methods, nil)}})}}
		rs, _ := check(rulesFile("2", Storage, body), Storage, DefaultLimits())
		for m := MethodGet; m <= MethodDelete; m++ {
			d := evaluate(t, rs, request(m, Literals("b", "bkt", "o", "x.png"), nil))
			if d.Allow != slices.Contains(c.grants, m) {
				t.Errorf("allow %s: %v %v", c.methods, m, d.Allow)
			}
			if !d.Allow && len(d.Statements) != 0 {
				t.Errorf("allow %s: %v evaluated a statement that does not cover it", c.methods, m)
			}
		}
	}
}

// Blocks are tried in file order, a block before the blocks in it, statements in order; each
// condition once; the first true one grants and nothing after it is evaluated. A block that
// takes part of the path grants nothing (rules do not cascade).
func TestDecisionOrder(t *testing.T) {
	docs := &memDocs{}
	look := func(v string) expr { return call("exists", ident(v)) }
	body := docsBlock(block{matches: []*match{
		matchb("/c/{id}", block{
			allows:  []*allow{allowb("read", look("request")), allowb("write", tru()), allowb("get", errExpr())},
			matches: []*match{matchb("/sub/{s}", block{allows: []*allow{allowb("read", tru())}})},
		}),
		matchb("/{all=**}", block{allows: []*allow{allowb("get", look("resource")), allowb("read", tru()), allowb("read", look("request"))}}),
		matchb("/c/{id}", block{allows: []*allow{allowb("get", tru())}}),
	}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	req := request(MethodGet, docPath("c", "d"), docs)
	req.Variables["request"] = docValue("x", "1")  // missing: exists is false
	req.Variables["resource"] = docValue("x", "2") // missing too
	d := evaluate(t, rs, req)
	var got []Outcome
	for _, s := range d.Statements {
		got = append(got, s.Outcome)
	}
	want := []Outcome{OutcomeFalse, OutcomeError, OutcomeFalse, OutcomeTrue}
	if !d.Allow || !slices.Equal(got, want) {
		t.Fatalf("allow %v, outcomes %v; want true, %v", d.Allow, got, want)
	}
	if !slices.Equal(d.Statements[1].Methods, []string{"get"}) || !slices.Equal(d.Statements[2].Methods, []string{"get"}) {
		t.Errorf("methods %v", d.Statements)
	}
	// One read each: the cached exists(request) of the last statement is never reached.
	if docs.reads["databases/(default)/documents/x/1"] != 1 || docs.reads["databases/(default)/documents/x/2"] != 1 || docs.total() != 2 {
		t.Errorf("reads %v, want one of each", docs.reads)
	}

	// The parent's statements do not reach the child's path; the child's do.
	d = evaluate(t, rs, request(MethodCreate, docPath("c", "d", "sub", "e"), nil))
	if d.Allow || len(d.Statements) != 0 {
		t.Errorf("a parent block cascaded: %+v", d)
	}
	if d := evaluate(t, rs, request(MethodList, docPath("c", "d", "sub", "e"), nil)); !d.Allow {
		t.Errorf("the child block denied: %+v", d)
	}
}

// A counter on every condition and on every read: each is evaluated once (no double evaluation
// as in the emulator, firebase-tools#6252).
func TestEachConditionOnce(t *testing.T) {
	docs := &memDocs{docs: map[string]Value{"databases/(default)/documents/x/1": Map(nil)}}
	look := call("exists", ident("request"))
	body := docsBlock(block{matches: []*match{
		matchb("/c/{id}", block{allows: []*allow{allowb("update", and(look, fls())), allowb("write", look)}}),
	}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	req := request(MethodUpdate, docPath("c", "d"), docs)
	req.Variables["request"] = docValue("x", "1")
	d := evaluate(t, rs, req)
	if !d.Allow || len(d.Statements) != 2 || docs.total() != 1 {
		t.Fatalf("allow %v, %d statements, %d reads; want true, 2, 1", d.Allow, len(d.Statements), docs.total())
	}
}

func TestTrace(t *testing.T) {
	src := "service firebase.storage {\n  match /b/{b}/o {\n    allow read, write: if 1;\n  }\n}\n"
	start := strings.Index(src, "allow")
	a := &allow{span: span{start, start + len("allow read, write: if 1;")}, methods: []name{{text: "read"}, {text: "write"}}, cond: lit(Int(1))}
	f := &file{name: "storage.rules", src: src, version: "2", service: "firebase.storage",
		body: block{matches: []*match{{path: segs("/b/{b}/o"), body: block{allows: []*allow{a}}}}}}
	rs := compile(t, Storage, f)
	req := request(MethodGet, Literals("b", "x", "o"), nil)
	d := evaluate(t, rs, req)
	want := StatementResult{Position: Position{Line: 3, Column: 5, CurrentOffset: 50, EndOffset: 74},
		Methods: []string{"read", "write"}, Outcome: OutcomeError, Message: "Type error. Received: [int] Expected: [bool]."}
	if d.Allow || len(d.Statements) != 1 || !equalResult(d.Statements[0], want) {
		t.Fatalf("%+v\nwant %+v", d.Statements, want)
	}
	req.Trace = TraceNone
	if d := evaluate(t, rs, req); d.Statements != nil {
		t.Errorf("traced without asking: %+v", d.Statements)
	}
}

func equalResult(a, b StatementResult) bool {
	return a.Position == b.Position && slices.Equal(a.Methods, b.Methods) && a.Outcome == b.Outcome && a.Message == b.Message
}

// A request the engine cannot decide is an outage, never a decision.
func TestInvalidRequest(t *testing.T) {
	body := docsBlock(block{matches: []*match{matchb("/{d=**}", block{allows: []*allow{allowb("read, write", nil)}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	long := make([]Segment, rs.lim.RequestSegments+1)
	for _, c := range []struct {
		name string
		edit func(r *Request)
	}{
		{"no Documents", func(r *Request) { r.Documents = nil }},
		{"no Budget", func(r *Request) { r.Budget = nil }},
		{"no Inflight", func(r *Request) { r.Inflight = nil }},
		{"no method", func(r *Request) { r.Method = 0 }},
		{"an unknown method", func(r *Request) { r.Method = MethodDelete + 1 }},
		{"a path past the limit", func(r *Request) { r.Path = long }},
	} {
		req := request(MethodGet, docPath("c", "d"), nil)
		c.edit(req)
		d, err := rs.Evaluate(context.Background(), req)
		if !errors.Is(err, errInvalidRequest) || d.Allow {
			t.Errorf("%s: %+v, %v", c.name, d, err)
		}
	}
	if _, err := rs.Evaluate(context.Background(), nil); err == nil {
		t.Error("a nil request decided")
	}
	if _, err := (*Ruleset)(nil).Evaluate(context.Background(), request(MethodGet, nil, nil)); err == nil {
		t.Error("a nil ruleset decided")
	}
	ok := request(MethodGet, long[:rs.lim.RequestSegments], nil)
	if _, err := rs.Evaluate(context.Background(), ok); err != nil {
		t.Errorf("a path at the limit: %v", err)
	}
}

// With nothing matching, or no statement for the method, the request is denied.
func TestNoMatchDenies(t *testing.T) {
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{allowb("read", nil)}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	for _, req := range []*Request{
		request(MethodGet, docPath("x", "d"), nil),
		request(MethodDelete, docPath("c", "d"), nil),
		request(MethodGet, nil, nil),
	} {
		if d := evaluate(t, rs, req); d.Allow || len(d.Statements) != 0 {
			t.Errorf("%v %v: %+v", req.Method, req.Path, d)
		}
	}
}

// A large batch: many operations each spend nearly their own expression budget and pass,
// because Aruma's counters are each evaluation's; the lookups are the request's, so the
// twenty-first in the batch ends its operation with a denial (ADR-021 §3).
func TestLargeBatch(t *testing.T) {
	fns := overLimit(7) // about 840 expressions
	body := block{functions: fns, matches: []*match{matchb("/{all=**}", block{allows: []*allow{
		allowb("write", and(call("overLimit0"), call("exists", ident("all")))),
	}})}}
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	docs := &memDocs{docs: map[string]Value{}}
	budget := NewBudget()
	for i := range 25 {
		id := string(rune('a' + i))
		docs.docs["databases/(default)/documents/c/"+id] = Map(nil)
		req := request(MethodCreate, docPath("c", id), docs)
		req.Budget = budget
		d := evaluate(t, rs, req)
		if d.Allow != (i < 20) {
			t.Fatalf("operation %d: allow %v (%+v)", i+1, d.Allow, d.Statements)
		}
	}
	if docs.total() != 20 {
		t.Errorf("%d reads, want 20", docs.total())
	}
}
