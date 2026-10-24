package mgmt

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// No more compilations than the slots run at once; the others wait their turn, and one whose
// request ends while it waits gives up with CANCELLED.
func TestCompileSlotsBoundConcurrency(t *testing.T) {
	if maxCompiles() < 2 {
		t.Fatalf("maxCompiles %d", maxCompiles())
	}
	a := &API{compiles: make(chan struct{}, 2)}
	var running, most atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			done, err := a.compileSlot(t.Context())
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

	hold1, _ := a.compileSlot(t.Context())
	hold2, _ := a.compileSlot(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.compileSlot(ctx); apierr.CodeOf(err) != apierr.Canceled {
		t.Errorf("waiting past the request's end: %v", err)
	}
	hold1()
	hold2()
	if done, err := a.compileSlot(t.Context()); err != nil {
		t.Errorf("after the slots are back: %v", err)
	} else {
		done()
	}
}
