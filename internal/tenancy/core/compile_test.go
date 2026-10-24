package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// No more compilations than the slots run at once; the others wait their turn, and one whose
// request ends while it waits gives up with CANCELLED.
func TestCompileSlotsBoundConcurrency(t *testing.T) {
	if maxCompiles() < 2 {
		t.Fatalf("maxCompiles %d", maxCompiles())
	}
	s := &Service{compiles: make(chan struct{}, 2)}
	var running, most atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			done, err := s.compileSlot(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			n := running.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			done()
		})
	}
	wg.Wait()
	if m := most.Load(); m != 2 {
		t.Errorf("%d compilations at once, want 2", m)
	}

	hold1, _ := s.compileSlot(t.Context())
	hold2, _ := s.compileSlot(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.compileSlot(ctx); apierr.CodeOf(err) != apierr.Canceled {
		t.Errorf("waiting past the request's end: %v", err)
	}
	hold1()
	hold2()
	if done, err := s.compileSlot(t.Context()); err != nil {
		t.Errorf("after the slots are back: %v", err)
	} else {
		done()
	}
}

// Every compilation takes a turn (the review's SR3-05), of one of two kinds. The management's:
// CompileRules, which mgmt's :test and the console's validate call, and CreateRuleset, which
// mgmt's createRuleset and the console's publish call, wait while those turns are taken and
// end with their request. A load of CompiledRelease never waits behind them (the review of
// S7-B1, SR3-05-dataplane-starvation): while every management turn is held, a cold release
// still loads; it waits only for the loads' turns.
func TestEveryCompilationTakesASlot(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	q, other := rulesOwner(t, s, pool, "x@example.com", "other-app")
	files := storageFile("true")
	publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, files)

	hold := func(turns chan struct{}) func() {
		var holds []func()
		for range cap(turns) {
			done, err := slot(t.Context(), turns)
			if err != nil {
				t.Fatal(err)
			}
			holds = append(holds, done)
		}
		return func() {
			for _, done := range holds {
				done()
			}
		}
	}

	release := hold(s.compiles)
	if rs, err := s.CompiledRelease(t.Context(), proj.Number, StorageRelease); err != nil || !allows(t, rs, proj.StorageBucket) {
		t.Errorf("a cold release while the management's turns are taken: %v", err)
	}
	for name, compile := range map[string]func(context.Context) error{
		"CompileRules": func(ctx context.Context) error {
			_, _, err := s.CompileRules(ctx, rules.Storage, files[0])
			return err
		},
		"CreateRuleset": func(ctx context.Context) error {
			_, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, files)
			return err
		},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		if err := compile(ctx); apierr.CodeOf(err) != apierr.Canceled {
			t.Errorf("%s while the management's turns are taken: %v", name, err)
		}
		cancel()
	}
	release()
	if page, err := s.ListRulesets(t.Context(), p, proj.ID, ListOptions{}); err != nil || len(page.Items) != 1 {
		t.Errorf("rulesets after a CreateRuleset that waited out its request: %v, %v", page.Items, err)
	}

	// The loads wait for their own turns, and the management does not.
	publish(t, s, q, other, "firebase.storage/"+other.StorageBucket, files)
	release = hold(s.loads)
	if rs, _, err := s.CompileRules(t.Context(), rules.Storage, files[0]); rs == nil || err != nil {
		t.Errorf("CompileRules while the loads' turns are taken: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	if _, err := s.CompiledRelease(ctx, other.Number, StorageRelease); err == nil {
		t.Error("a release loaded while the loads' turns are taken")
	}
	cancel()
	release()
	if rs, err := s.CompiledRelease(t.Context(), other.Number, StorageRelease); err != nil || !allows(t, rs, other.StorageBucket) {
		t.Errorf("after the loads' turns are back: %v", err)
	}
}
