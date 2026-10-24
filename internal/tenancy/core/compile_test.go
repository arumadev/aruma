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

// Every compilation takes a turn of the same slots (the review's SR3-05): CompileRules, which
// mgmt's :test and the console's validate call, CreateRuleset, which mgmt's createRuleset and
// the console's publish call, and the load of a release CompiledRelease compiles. Each waits
// while the slots are taken and ends with its request.
func TestEveryCompilationTakesASlot(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	files := storageFile("true")
	publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, files)

	var holds []func()
	for range maxCompiles() {
		done, err := s.compileSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		holds = append(holds, done)
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
			t.Errorf("%s while the slots are taken: %v", name, err)
		}
		cancel()
	}
	// The load runs on its own; the request stops waiting for it at its end.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	if _, err := s.CompiledRelease(ctx, proj.Number, StorageRelease); err == nil {
		t.Error("CompiledRelease compiled while the slots are taken")
	}
	cancel()
	for _, done := range holds {
		done()
	}
	if rs, err := s.CompiledRelease(t.Context(), proj.Number, StorageRelease); err != nil || !allows(t, rs, proj.StorageBucket) {
		t.Errorf("after the slots are back: %v", err)
	}
	if page, err := s.ListRulesets(t.Context(), p, proj.ID, ListOptions{}); err != nil || len(page.Items) != 1 {
		t.Errorf("rulesets after a CreateRuleset that waited out its request: %v, %v", page.Items, err)
	}
}
