package core

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// waiters returns how many requests hold or wait for a session's lock, and whether one holds it.
func (l *sessionLocks) waiters(idHash []byte) (refs int, held bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.held[string(idHash)]
	if e == nil {
		return 0, false
	}
	return e.refs, len(e.held) == 1
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// One request holds a session, trickling its body; a hundred more on the same session URL wait
// for its lock. When their clients go, their contexts end, and every waiter leaves at once with
// ErrUnavailable instead of waiting for the holder: the goroutines come back to where they were.
// The holder is untouched, and the session goes on.
func TestSessionLockWaitersLeaveWithTheirContext(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, asAdmin(), uploadSpec("slow.bin", SurfaceGCS))
	idHash := e.s.keys.HashToken(id)

	pr, pw := io.Pipe()
	holder := make(chan error, 1)
	go func() {
		_, err := e.s.WriteUpload(context.Background(), e.a, "slow.bin", id, nil,
			Chunk{Body: pr, Length: -1, Total: -1})
		holder <- err
	}()
	if _, err := pw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the holder takes the lock", func() bool { _, held := e.s.sessions.waiters(idHash); return held })

	baseline := runtime.NumGoroutine()
	const n = 100
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := e.s.WriteUpload(ctx, e.a, "slow.bin", id, nil,
				Chunk{Offset: 3, Body: strings.NewReader("def"), Length: 3, Total: -1})
			errs <- err
		})
	}
	eventually(t, "the requests queue on the lock", func() bool { refs, _ := e.s.sessions.waiters(idHash); return refs == n+1 })
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the waiters did not leave when their contexts ended")
	}
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
			t.Fatalf("a waiter got %v, want ErrUnavailable from its context", err)
		}
	}
	eventually(t, "the goroutines come back to the baseline", func() bool { return runtime.NumGoroutine() <= baseline })
	if refs, held := e.s.sessions.waiters(idHash); refs != 1 || !held {
		t.Fatalf("after the waiters: refs %d, held %v", refs, held)
	}

	// The holder finishes its chunk, and the session takes the next one.
	_ = pw.Close()
	if err := <-holder; err != nil {
		t.Fatalf("the holder: %v", err)
	}
	st, err := e.chunk(e.a, "slow.bin", id, 3, "def", true)
	if err != nil || st.Object == nil || st.Object.Size != 6 {
		t.Fatalf("finalize: %+v, %v", st, err)
	}
	if refs, _ := e.s.sessions.waiters(idHash); refs != 0 {
		t.Fatalf("the entry stays with %d refs", refs)
	}
}

// A context already ended does not take a free lock's place for long, and tryLock keeps its
// meaning: it fails while the lock is held and does not wait.
func TestSessionLockTryLockAndCancel(t *testing.T) {
	var l sessionLocks
	k := []byte("session")
	unlock, err := l.lock(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.tryLock(k); ok {
		t.Fatal("tryLock took a held lock")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.lock(ctx, k); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lock with an ended context on a held lock: %v", err)
	}
	unlock()
	release, ok := l.tryLock(k)
	if !ok {
		t.Fatal("tryLock failed on a free lock")
	}
	release()
	if refs, _ := l.waiters(k); refs != 0 || len(l.held) != 0 {
		t.Fatalf("entries left: %d %v", refs, l.held)
	}
}
