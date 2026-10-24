package rules

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Many requests at once on one Ruleset, under -race: matching, functions, lists and lookups,
// some sharing a Budget as the operations of a batch do. Every decision is the one a request
// alone gets. Patterns built during evaluation join this test when S1-B's library merges.
func TestConcurrentEvaluate(t *testing.T) {
	isOwner := fn("isOwner", []string{"p"}, []*let{letb("seen", call("exists", ident("p")))}, ident("seen"))
	body := block{functions: []*function{isOwner}, matches: []*match{
		matchb("/{all=**}", block{allows: []*allow{
			allowb("get", and(or(list(tru(), ident("all")), tru()), call("isOwner", ident("all")))),
			allowb("list", ternary(call("exists", ident("all")), tru(), errExpr())),
		}}),
	}}
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	docs := &memDocs{docs: map[string]Value{}}
	for i := range 50 {
		if i%2 == 0 {
			docs.docs[fmt.Sprintf("databases/(default)/documents/c/%d", i)] = Map(nil)
		}
	}
	inflight := NewInflight(64 << 20)
	// One batch's budget, shared by every goroutine for ten documents it has already read, so
	// that its cache is read concurrently and its count never reaches the limit.
	shared := NewBudget()
	for n := range 10 {
		req := request(MethodGet, docPath("c", fmt.Sprint(n)), docs)
		req.Budget = shared
		evaluate(t, rs, req)
	}
	var wg sync.WaitGroup
	var failures atomic.Int64
	for g := range 16 {
		wg.Go(func() {
			for i := range 200 {
				n := (g*7 + i) % 50
				m := MethodGet
				if i%3 == 0 {
					m = MethodList
				}
				req := request(m, docPath("c", fmt.Sprint(n)), docs)
				req.Inflight = inflight
				if n < 10 {
					req.Budget = shared
				}
				d, err := rs.Evaluate(context.Background(), req)
				if err != nil || d.Allow != (n%2 == 0) {
					failures.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("%d decisions differ from the request alone", n)
	}
	if used := inflight.used.Load(); used != 0 {
		t.Fatalf("%d bytes still reserved", used)
	}
}

// Concurrent evaluations that together reach the Inflight cap: the one past it gets
// ErrOverloaded, not a denial, and once they end everything is returned and the next one is
// decided (ADR-021 §4).
func TestInflightIsBounded(t *testing.T) {
	const holding = 4
	elems := make([]expr, 100)
	for i := range elems {
		elems[i] = tru()
	}
	body := block{matches: []*match{matchb("/{all=**}", block{allows: []*allow{
		allowb("get", and(or(list(elems...), tru()), call("exists", ident("all")))),
	}})}}
	rs := compile(t, Firestore, rulesFile("2", Firestore, body))
	perEval := int64(len(elems))*valueBytes + 5*16 + 24 // the list, and the path {all=**} took
	inflight := NewInflight(holding * perEval)

	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(holding)
	docs := &memDocs{onGet: func([]string) { entered.Done(); <-release }}
	var wg sync.WaitGroup
	results := make([]error, holding)
	for i := range holding {
		wg.Go(func() {
			req := request(MethodGet, docPath("c", fmt.Sprint(i)), docs)
			req.Inflight = inflight
			_, results[i] = rs.Evaluate(context.Background(), req)
		})
	}
	// Each holds its list, waiting in its lookup.
	holdingAll := make(chan struct{})
	go func() { entered.Wait(); close(holdingAll) }()
	select {
	case <-holdingAll:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("the evaluations did not reach their lookups")
	}

	req := request(MethodGet, docPath("c", "x"), &memDocs{})
	req.Inflight = inflight
	d, err := rs.Evaluate(context.Background(), req)
	if !errors.Is(err, ErrOverloaded) || d.Allow {
		t.Fatalf("past the cap: %+v, %v; want ErrOverloaded", d, err)
	}
	close(release)
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Errorf("evaluation %d within the cap: %v", i, err)
		}
	}
	if used := inflight.used.Load(); used != 0 {
		t.Fatalf("%d bytes still reserved after every evaluation ended", used)
	}
	if _, err := rs.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("the next evaluation: %v", err)
	}
}
