package rules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// S1-C's tests build trees by hand until the parser (S1-A) and the library (S1-B) merge, and use
// only what goes through neither: literals, identifiers, &&, ||, !, ?:, lists, maps, functions,
// lets, lookups and match paths.

func lit(v Value) expr                 { return &litExpr{val: v} }
func tru() expr                        { return lit(Bool(true)) }
func fls() expr                        { return lit(Bool(false)) }
func ident(n string) *identExpr        { return &identExpr{name: n} }
func and(x, y expr) expr               { return &binaryExpr{op: tokAnd, x: x, y: y} }
func or(x, y expr) expr                { return &binaryExpr{op: tokOr, x: x, y: y} }
func not(x expr) expr                  { return &unaryExpr{op: tokNot, x: x} }
func ternary(c, a, b expr) expr        { return &condExpr{test: c, yes: a, no: b} }
func listx(xs ...expr) expr            { return &listExpr{elems: xs} }
func call(n string, args ...expr) expr { return &callExpr{fn: ident(n), args: args} }
func nscall(ns, n string, args ...expr) expr {
	return &callExpr{fn: &memberExpr{x: ident(ns), name: n}, args: args}
}

// errExpr is an expression that fails when evaluated: a call of a function that does not exist
// ("Function not found error"), which the checker only warns about.
func errExpr() expr { return call("nope") }

func fn(n string, params []string, lets []*let, ret expr) *function {
	f := &function{name: name{text: n}, lets: lets, ret: ret}
	for _, p := range params {
		f.params = append(f.params, name{text: p})
	}
	return f
}

func letb(n string, v expr) *let { return &let{name: name{text: n}, value: v} }

func lseg(s string) pattern { return pattern{kind: patternLiteral, text: s} }
func vseg(s string) pattern { return pattern{kind: patternVariable, text: s} }
func gseg(s string) pattern { return pattern{kind: patternRest, text: s} }

// segs reads "a/{b}/{c=**}" as patterns.
func segs(path string) []pattern {
	var ps []pattern
	for s := range strings.SplitSeq(strings.Trim(path, "/"), "/") {
		switch {
		case strings.HasSuffix(s, "=**}"):
			ps = append(ps, gseg(strings.TrimSuffix(s[1:], "=**}")))
		case strings.HasPrefix(s, "{"):
			ps = append(ps, vseg(strings.Trim(s, "{}")))
		default:
			ps = append(ps, lseg(s))
		}
	}
	return ps
}

func matchb(path string, body block) *match { return &match{path: segs(path), body: body} }

func allowb(methods string, cond expr) *allow {
	a := &allow{cond: cond}
	for m := range strings.SplitSeq(methods, ",") {
		a.methods = append(a.methods, name{text: strings.TrimSpace(m)})
	}
	return a
}

// rulesFile is a file of version (empty for none) for svc.
func rulesFile(version string, svc Service, body block) *file {
	return &file{name: "test.rules", version: version, service: string(svc), body: body}
}

// docsBlock wraps blocks in Firestore's root, match /databases/{database}/documents.
func docsBlock(b block) block {
	return block{matches: []*match{matchb("/databases/{database}/documents", b)}}
}

// compile checks f for svc and fails the test on an error.
func compile(t *testing.T, svc Service, f *file) *Ruleset {
	t.Helper()
	rs, issues := check(f, svc, DefaultLimits())
	for _, is := range issues {
		if is.Severity == SeverityError {
			t.Fatalf("check: %s", is.Description)
		}
	}
	if rs == nil {
		t.Fatal("check returned no ruleset")
	}
	return rs
}

// docPath is the request path of a Firestore document.
func docPath(segs ...string) []Segment {
	return Literals(append([]string{"databases", "(default)", "documents"}, segs...)...)
}

func docValue(segs ...string) Value {
	return PathOf(append([]string{"databases", "(default)", "documents"}, segs...))
}

// memDocs is an in-memory Documents that counts its reads.
type memDocs struct {
	mu       sync.Mutex
	docs     map[string]Value // by path joined with "/"
	reads    map[string]int
	projects []int64
	err      error               // returned by every read when set
	onGet    func(path []string) // called on every read
}

func (d *memDocs) Get(ctx context.Context, project int64, path []string, after bool) (Value, bool, error) {
	d.mu.Lock()
	key := strings.Join(path, "/")
	if after {
		key += "@after"
	}
	if d.reads == nil {
		d.reads = map[string]int{}
	}
	d.reads[key]++
	d.projects = append(d.projects, project)
	onGet, err := d.onGet, d.err
	v, ok := d.docs[strings.Join(path, "/")]
	d.mu.Unlock()
	if onGet != nil {
		onGet(path)
	}
	if err != nil {
		return Value{}, false, err
	}
	return v, ok, nil
}

func (d *memDocs) total() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.reads {
		n += c
	}
	return n
}

// request is a traced request with fresh Budget and Inflight.
func request(m Method, path []Segment, docs Documents) *Request {
	if docs == nil {
		docs = &memDocs{}
	}
	return &Request{
		Project: 7, Method: m, Path: path, Variables: map[string]Value{},
		Documents: docs, Budget: NewBudget(), Inflight: NewInflight(512 << 20), Trace: TraceStatements,
	}
}

func evaluate(t *testing.T, rs *Ruleset, req *Request) Decision {
	t.Helper()
	d, err := rs.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return d
}

// one compiles a Firestore file with a single block /c/{id} granting get when cond holds, and
// returns the outcome of that one statement.
func one(t *testing.T, cond expr, fns ...*function) StatementResult {
	t.Helper()
	body := docsBlock(block{functions: fns, matches: []*match{matchb("/c/{id}", block{allows: []*allow{allowb("get", cond)}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil))
	if len(d.Statements) != 1 {
		t.Fatalf("%d statements evaluated, want 1", len(d.Statements))
	}
	if d.Allow != (d.Statements[0].Outcome == OutcomeTrue) {
		t.Fatalf("Allow %v with outcome %v", d.Allow, d.Statements[0].Outcome)
	}
	return d.Statements[0]
}

// The error-absorption table of ADR-021 §2.3, case by case, with a non-boolean operand an error
// like any other (cel-spec logic.textproto, hidane, Pyric strict-boolean-control-flow and
// error-absorption-and-or; confirmed by S0-A).
func TestErrorAbsorption(t *testing.T) {
	E := errExpr
	horses := func() expr { return lit(String("horses")) }
	n32 := func() expr { return lit(Int(32)) }
	for _, c := range []struct {
		name string
		cond expr
		want Outcome
	}{
		{"true || E", or(tru(), E()), OutcomeTrue},
		{"false && E", and(fls(), E()), OutcomeFalse},
		{"false || true", or(fls(), tru()), OutcomeTrue},
		{"false || false", or(fls(), fls()), OutcomeFalse},
		{"true && true", and(tru(), tru()), OutcomeTrue},
		{"true && false", and(tru(), fls()), OutcomeFalse},
		{"false || E", or(fls(), E()), OutcomeError},
		{"true && E", and(tru(), E()), OutcomeError},
		{"false || 32", or(fls(), n32()), OutcomeError},
		{"true && 'horses'", and(tru(), horses()), OutcomeError},
		{"E || true", or(E(), tru()), OutcomeTrue},
		{"E || false", or(E(), fls()), OutcomeError},
		{"E || E", or(E(), E()), OutcomeError},
		{"E && false", and(E(), fls()), OutcomeFalse},
		{"E && true", and(E(), tru()), OutcomeError},
		{"'horses' || true", or(horses(), tru()), OutcomeTrue},
		{"'horses' || false", or(horses(), fls()), OutcomeError},
		{"32 && false", and(n32(), fls()), OutcomeFalse},
		{"32 && true", and(n32(), tru()), OutcomeError},
		{"(E && false) || true", or(and(E(), fls()), tru()), OutcomeTrue},
		{"(E || false) && false", and(or(E(), fls()), fls()), OutcomeFalse},
		{"!E", not(E()), OutcomeError},
		{"!true", not(tru()), OutcomeFalse},
		{"!false", not(fls()), OutcomeTrue},
		{"!!true", not(not(tru())), OutcomeTrue},
		{"E ? true : true", ternary(E(), tru(), tru()), OutcomeError},
		{"'horses' ? true : true", ternary(horses(), tru(), tru()), OutcomeError},
		{"true ? true : E", ternary(tru(), tru(), E()), OutcomeTrue},
		{"false ? E : true", ternary(fls(), E(), tru()), OutcomeTrue},
		{"true ? E : true", ternary(tru(), E(), tru()), OutcomeError},
		{"true ? 32 : true", ternary(tru(), n32(), tru()), OutcomeError},
		{"a condition that is not a boolean", n32(), OutcomeError},
		{"a list", listx(tru()), OutcomeError},
		{"E in a list", listx(E(), tru()), OutcomeError},
		{"null", lit(Null()), OutcomeError},
		{"true", tru(), OutcomeTrue},
		{"false", fls(), OutcomeFalse},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := one(t, c.cond); got.Outcome != c.want {
				t.Errorf("outcome %v (%q), want %v", got.Outcome, got.Message, c.want)
			}
		})
	}
}

func TestErrorMessages(t *testing.T) {
	for _, c := range []struct {
		name string
		cond expr
		want string
	}{
		{"an unknown function", errExpr(), "Function not found error: Name: [nope]."},
		{"a non-boolean right of &&", and(tru(), lit(Int(32))), "Type error. Received: [int] Expected: [bool]."},
		{"a non-boolean left of || kept when the right is false", or(lit(String("x")), fls()), "Type error. Received: [string] Expected: [bool]."},
		{"the left error kept", or(errExpr(), lit(Int(1))), "Function not found error: Name: [nope]."},
		{"a non-boolean condition", lit(Null()), "Type error. Received: [null] Expected: [bool]."},
		{"an unknown variable", ident("nobody"), "Null value error."},
		{"a map key that is not a string", or(&mapExpr{keys: []expr{lit(Int(1))}, vals: []expr{tru()}}, fls()), "Type error. Received: [int] Expected: [string]."},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := one(t, c.cond)
			if got.Outcome != OutcomeError || got.Message != c.want {
				t.Errorf("%v %q, want an error %q", got.Outcome, got.Message, c.want)
			}
		})
	}
}

// Arguments and lets are evaluated before the function's result and bound to a value or an
// error, which fails only where the function reads it (Pyric, error-absorption-and-or and
// functions-let-scope; confirmed by S0-A).
func TestArgumentsAndLets(t *testing.T) {
	fns := []*function{
		fn("ignoresArg", []string{"a"}, nil, tru()),
		fn("readsArg", []string{"a"}, nil, ident("a")),
		fn("passesToIgnoresArg", []string{"a"}, nil, call("ignoresArg", ident("a"))),
		fn("passesToReadsArg", []string{"a"}, nil, call("readsArg", ident("a"))),
		fn("readsSecondArg", []string{"a", "b"}, nil, ident("b")),
		fn("letUnread", nil, []*let{letb("x", errExpr())}, tru()),
		fn("letRead", nil, []*let{letb("x", errExpr())}, ident("x")),
		fn("letChain", nil, []*let{letb("x", tru()), letb("y", ident("x"))}, ident("y")),
		fn("letShadowsVariable", nil, []*let{letb("id", fls())}, ident("id")),
		fn("readsPathVar", nil, nil, ternary(ident("id"), tru(), fls())),
	}
	for _, c := range []struct {
		cond expr
		want Outcome
	}{
		{call("ignoresArg", errExpr()), OutcomeTrue},
		{call("readsArg", errExpr()), OutcomeError},
		{call("passesToIgnoresArg", errExpr()), OutcomeTrue},
		{call("passesToReadsArg", errExpr()), OutcomeError},
		{call("readsSecondArg", errExpr(), tru()), OutcomeTrue},
		{call("readsSecondArg", tru(), errExpr()), OutcomeError},
		{call("readsSecondArg", ident("nobody"), fls()), OutcomeFalse},
		{call("letUnread"), OutcomeTrue},
		{call("letRead"), OutcomeError},
		{call("letChain"), OutcomeTrue},
		{call("letShadowsVariable"), OutcomeFalse},
		{call("readsPathVar"), OutcomeError},           // id is a string: not a condition
		{call("readsArg", tru(), tru()), OutcomeError}, // the wrong number of arguments
		{or(call("readsArg"), tru()), OutcomeTrue},
	} {
		got := one(t, c.cond, fns...)
		if got.Outcome != c.want {
			t.Errorf("%s: %v (%q), want %v", describe(c.cond), got.Outcome, got.Message, c.want)
		}
	}
}

// describe names a call for messages.
func describe(e expr) string {
	if c, ok := e.(*callExpr); ok {
		if id, ok := c.fn.(*identExpr); ok {
			return id.name + "(…)"
		}
	}
	return fmt.Sprintf("%T", e)
}

// Arguments and lets are evaluated even when unread: their lookups are made (the emulator logs
// a debug() in an unused let; hidane).
func TestUnreadArgumentsAreEvaluated(t *testing.T) {
	docs := &memDocs{}
	f := fn("ignores", []string{"a"}, []*let{letb("x", call("exists", ident("resource")))}, tru())
	body := docsBlock(block{functions: []*function{f}, matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("get", call("ignores", call("exists", ident("request")))),
	}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	req := request(MethodGet, docPath("c", "d"), docs)
	req.Variables["request"] = docValue("a", "1")
	req.Variables["resource"] = docValue("b", "2")
	if d := evaluate(t, rs, req); !d.Allow {
		t.Fatalf("denied: %+v", d.Statements)
	}
	if docs.total() != 2 {
		t.Errorf("%d reads, want the argument's and the let's: %v", docs.total(), docs.reads)
	}
}

// Short-circuits and the branch ?: does not choose make no lookups.
func TestUnevaluatedBranchesMakeNoLookups(t *testing.T) {
	look := func() expr { return call("exists", ident("resource")) }
	for _, c := range []struct {
		name  string
		cond  expr
		reads int
	}{
		{"true || lookup", or(tru(), look()), 0},
		{"false && lookup", and(fls(), look()), 0},
		{"true ? true : lookup", ternary(tru(), tru(), look()), 0},
		{"false ? lookup : true", ternary(fls(), look(), tru()), 0},
		{"false || lookup", or(fls(), look()), 1},
		{"E || lookup", or(errExpr(), look()), 1},
		{"an unknown function's arguments", call("nope", look()), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			docs := &memDocs{}
			body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{allowb("get", c.cond)}})}})
			rs := compile(t, Firestore, rulesFile("2", Firestore, body))
			req := request(MethodGet, docPath("c", "d"), docs)
			req.Variables["resource"] = docValue("x", "y")
			evaluate(t, rs, req)
			if docs.total() != c.reads {
				t.Errorf("%d reads, want %d", docs.total(), c.reads)
			}
		})
	}
}

// overLimit is Pyric's over-limit chain: ten functions of 40 true conjuncts, each calling the
// next from its last, true and about 1,210 expressions in all.
func overLimit(n int) []*function {
	var fns []*function
	for i := range n {
		var e expr = tru()
		for range 39 {
			e = and(e, tru())
		}
		if i < n-1 {
			e = and(e, call(fmt.Sprintf("overLimit%d", i+1)))
		}
		fns = append(fns, fn(fmt.Sprintf("overLimit%d", i), nil, nil, e))
	}
	return fns
}

// The expression limit is the whole evaluation's, across allow statements, and going past it
// denies even a true condition, and even after an error that || would absorb (ADR-021 §4;
// Pyric: "an error, then a true rule past the expression limit → DENY").
func TestExpressionLimit(t *testing.T) {
	if got := one(t, call("overLimit0"), overLimit(5)...); got.Outcome != OutcomeTrue {
		t.Fatalf("five functions (about 600 expressions): %v %q", got.Outcome, got.Message)
	}
	got := one(t, or(errExpr(), call("overLimit0")), overLimit(10)...)
	if got.Outcome != OutcomeError || !strings.Contains(got.Message, "maximum of 1000 expressions") {
		t.Fatalf("ten functions: %v %q, want the limit", got.Outcome, got.Message)
	}

	// Across statements of two blocks: an error spending about 600, then a true one doing the same.
	fns := append(overLimit(5), fn("fails", nil, nil, and(call("overLimit0"), errExpr())))
	body := docsBlock(block{functions: fns, matches: []*match{
		matchb("/c/{id}", block{allows: []*allow{allowb("get", call("fails"))}}),
		matchb("/c/{id}", block{allows: []*allow{allowb("get", call("overLimit0"))}}),
	}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil))
	if d.Allow || len(d.Statements) != 2 || d.Statements[0].Outcome != OutcomeError ||
		!strings.Contains(d.Statements[1].Message, "maximum of 1000") {
		t.Fatalf("allowed past the limit across statements: %+v", d)
	}

	// Exactly at the limit: k true literals joined by && cost 3k-2 (each && one more for its
	// right side), so 334 cost 1000 and 335 cost 1003.
	chain := func(k int) expr {
		var e expr = tru()
		for range k - 1 {
			e = and(e, tru())
		}
		return e
	}
	if got := one(t, chain(334)); got.Outcome != OutcomeTrue {
		t.Errorf("1000 expressions: %v %q", got.Outcome, got.Message)
	}
	if got := one(t, chain(335)); got.Outcome != OutcomeError {
		t.Errorf("1003 expressions: %v", got.Outcome)
	}
}

// An Aruma limit ends the evaluation with a denial that || cannot absorb; an evaluation error
// it can (ADR-021 §4, abort against evalError).
func TestAbortIsNotAbsorbed(t *testing.T) {
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("get", or(listx(tru()), tru())),
	}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	if d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil)); !d.Allow {
		t.Fatalf("a list absorbed by || denied: %+v", d)
	}
	rs.lim.AllocBytes = 8 // below one element
	d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil))
	if d.Allow || len(d.Statements) != 1 || !strings.Contains(d.Statements[0].Message, "allocation limit") {
		t.Fatalf("an allocation past the limit was absorbed: %+v", d)
	}
}

// Every 64 expressions the context is polled: a request its client left stops, with the
// context's error rather than a decision.
func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	docs := &memDocs{docs: map[string]Value{"databases/(default)/documents/x/y": Map(nil)}, onGet: func([]string) { cancel() }}
	var tail expr = tru()
	for range 70 {
		tail = and(tail, tru())
	}
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("get", and(call("exists", ident("resource")), tail)),
		allowb("get", tru()),
	}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	req := request(MethodGet, docPath("c", "d"), docs)
	req.Variables["resource"] = docValue("x", "y")
	d, err := rs.Evaluate(ctx, req)
	if !errors.Is(err, context.Canceled) || d.Allow {
		t.Fatalf("Evaluate = %+v, %v; want context.Canceled", d, err)
	}

	// Before a lookup too, and before anything when it is already done.
	ctx, cancel = context.WithCancel(context.Background())
	docs = &memDocs{onGet: func([]string) { cancel() }}
	body = docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("get", or(call("exists", ident("resource")), call("exists", ident("request")))),
	}})}})
	rs = compile(t, Firestore, rulesFile("2", Firestore, body))
	req = request(MethodGet, docPath("c", "d"), docs)
	req.Variables["resource"] = docValue("x", "y")
	req.Variables["request"] = docValue("x", "z")
	if _, err := rs.Evaluate(ctx, req); !errors.Is(err, context.Canceled) || docs.total() != 1 {
		t.Fatalf("Evaluate = %v after %d reads; want context.Canceled after 1", err, docs.total())
	}
	if _, err := rs.Evaluate(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("a done context: %v", err)
	}
}

// A full Inflight is an outage, never a denial (ADR-021 §4), and what an evaluation reserved
// is returned when it ends.
func TestOverloadedIsAnOutage(t *testing.T) {
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("get", or(listx(tru(), tru()), tru())),
	}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	req := request(MethodGet, docPath("c", "d"), nil)
	req.Inflight = NewInflight(valueBytes)
	d, err := rs.Evaluate(context.Background(), req)
	if !errors.Is(err, ErrOverloaded) || d.Allow {
		t.Fatalf("Evaluate = %+v, %v; want ErrOverloaded", d, err)
	}
	req.Inflight = NewInflight(2 * valueBytes)
	if d := evaluate(t, rs, req); !d.Allow {
		t.Fatalf("denied with room: %+v", d)
	}
	if used := req.Inflight.used.Load(); used != 0 {
		t.Errorf("%d bytes still reserved after the evaluation", used)
	}
}

// A path variable that took an unknown segment has no value: reading it is an error without
// a message, and leaving it unread changes nothing (ADR-021 §2.4).
func TestUnboundPathVariable(t *testing.T) {
	body := docsBlock(block{matches: []*match{matchb("/c/{id}", block{allows: []*allow{
		allowb("list", or(ident("id"), fls())),
		allowb("list", or(ident("id"), tru())),
	}})}})
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	path := append(docPath("c"), UnknownSegment)
	d := evaluate(t, rs, request(MethodList, path, nil))
	if !d.Allow || d.Statements[0].Outcome != OutcomeError || d.Statements[0].Message != "" {
		t.Fatalf("%+v", d)
	}
}
