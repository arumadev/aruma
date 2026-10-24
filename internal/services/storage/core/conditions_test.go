package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// A rewrite's source conditions hold on the row whose file it copies (S4-A's review): a source
// replaced after a caller looked at it is not copied in its place.
func TestRewriteSourceConditions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v1 := e.insert(e.a, "src.txt", "one")
	v2 := e.insert(e.a, "src.txt", "two")
	stale := []SourceConditions{
		{Generation: v1.Generation},
		{Pre: Preconditions{IfGenerationMatch: new(v1.Generation)}},
		{Pre: Preconditions{IfGenerationNotMatch: new(v2.Generation)}},
		{Pre: Preconditions{IfMetagenerationMatch: new(int64(2))}},
		{Pre: Preconditions{IfMetagenerationNotMatch: new(int64(1))}},
		{Generation: v2.Generation, Pre: Preconditions{IfGenerationMatch: new(v1.Generation)}},
	}
	for i, c := range stale {
		_, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "dst.txt", asAdmin(), nil, Preconditions{}, c)
		want := ErrPreconditionFailed
		if i == 0 {
			want = ErrNotFound // a generation that is not the current one
		}
		if !errors.Is(err, want) {
			t.Errorf("%+v: %v, want %v", c, err, want)
		}
	}
	if e.count(`SELECT count(*) FROM storage.objects WHERE name = 'dst.txt'`) != 0 {
		t.Fatal("a refused rewrite left a destination")
	}
	cp, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "dst.txt", asAdmin(), nil, Preconditions{},
		SourceConditions{Generation: v2.Generation, Pre: Preconditions{IfGenerationMatch: new(v2.Generation), IfMetagenerationMatch: new(int64(1))}})
	if err != nil || cp.CRC32C != v2.CRC32C || e.read(e.a, "dst.txt") != "two" {
		t.Fatalf("the current generation: %+v, %v", cp, err)
	}
	for _, c := range [][]SourceConditions{{{}, {}}, {{Generation: -1}}} {
		if _, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "x", asAdmin(), nil, Preconditions{}, c...); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if _, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "x", asAdmin(), nil, Preconditions{}); err != nil {
		t.Errorf("without conditions: %v", err)
	}
	e.checkUsage(projectA)
}

// The race the review reproduced: the source is replaced while rewrites conditioned on the
// generation a caller saw run. Each copy that succeeds holds that generation's bytes, never the
// next one's.
func TestRewriteSourceConditionsUnderReplacement(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cur := e.insert(e.a, "src.txt", "body-0")
	bodies := map[int64]string{cur.Generation: "body-0"}
	var mu sync.Mutex
	for i := range 20 {
		seen := cur.Generation
		var wg sync.WaitGroup
		var cerr error
		dst := fmt.Sprintf("dst-%d", i)
		wg.Go(func() {
			_, cerr = e.s.Rewrite(ctx, e.a, "src.txt", e.a, dst, asAdmin(), nil, Preconditions{},
				SourceConditions{Pre: Preconditions{IfGenerationMatch: new(seen)}})
		})
		wg.Go(func() {
			body := fmt.Sprintf("body-%d", i+1)
			o := e.insert(e.a, "src.txt", body)
			mu.Lock()
			bodies[o.Generation] = body
			cur = o
			mu.Unlock()
		})
		wg.Wait()
		switch {
		case cerr == nil:
			if got := e.read(e.a, dst); got != bodies[seen] {
				t.Fatalf("round %d: a rewrite conditioned on %d copied %q, want %q", i, seen, got, bodies[seen])
			}
		case !errors.Is(cerr, ErrPreconditionFailed):
			t.Fatalf("round %d: %v", i, cerr)
		}
	}
	e.checkUsage(projectA)
}

// SetCORS's conditions hold on the settings row it locks (S4-A's review): of concurrent
// changes with the same ifMetagenerationMatch, exactly one passes.
func TestSetCORSConditions(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rules := []CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}}}
	admin := Principal{Kind: PrincipalAdmin}

	// A condition that fails on a bucket never changed leaves no settings row behind.
	if _, err := f.s.SetCORS(ctx, f.b, admin, rules, BucketConditions{IfMetagenerationMatch: new(int64(5))}); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("ifMetagenerationMatch=5 on 1: %v", err)
	}
	if info, err := f.s.BucketInfo(ctx, f.b); err != nil || info.Metageneration != 1 || !info.Updated.Equal(created) || info.CORS != nil {
		t.Fatalf("after the refusal: %+v, %v", info, err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			_, errs[i] = f.s.SetCORS(ctx, f.b, admin, rules, BucketConditions{IfMetagenerationMatch: new(int64(1))})
		})
	}
	wg.Wait()
	passed := 0
	for _, err := range errs {
		switch {
		case err == nil:
			passed++
		case !errors.Is(err, ErrPreconditionFailed):
			t.Fatalf("a concurrent change: %v", err)
		}
	}
	if info, err := f.s.BucketInfo(ctx, f.b); passed != 1 || err != nil || info.Metageneration != 2 {
		t.Fatalf("%d of %d passed ifMetagenerationMatch=1; the bucket: %+v, %v", passed, n, info, err)
	}

	if _, err := f.s.SetCORS(ctx, f.b, admin, nil, BucketConditions{IfMetagenerationNotMatch: new(int64(2))}); !errors.Is(err, ErrPreconditionFailed) {
		t.Errorf("ifMetagenerationNotMatch=2 on 2: %v", err)
	}
	if info, err := f.s.SetCORS(ctx, f.b, admin, nil, BucketConditions{IfMetagenerationNotMatch: new(int64(1)), IfMetagenerationMatch: new(int64(2))}); err != nil || info.Metageneration != 3 || info.CORS != nil {
		t.Errorf("conditions that hold: %+v, %v", info, err)
	}
	if _, err := f.s.SetCORS(ctx, f.b, admin, nil, BucketConditions{}, BucketConditions{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("two conditions: %v", err)
	}
}
