package core

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/blob"
)

// S2-D on S2-A's Disk and lock rather than the in-memory store: a session's life, an overwrite
// and its GC, a restart's sweep, and a second instance waiting on flock. Until S2-A is merged
// the contract's Disk panics, and the test skips, naming it.

func onDisk(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	_ = e.s.Close()
	acquireLock = blob.AcquireLock
	dir := t.TempDir()
	disk, err := blob.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.opts.DataDir = dir
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM storage.instance`); err != nil {
		t.Fatal(err)
	}
	e.s, err = New(context.Background(), e.pool, disk, e.opts, slog.New(slog.NewTextHandler(e.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e.s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	if msg := pendingOn(func() { err = e.s.Boot(context.Background()) }); msg != "" {
		t.Skipf("the disk is not written yet: %s", msg)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.s.Close() })
	return e, dir
}

// pendingOn runs f and returns the owner's message when it hits a contract body that panics.
func pendingOn(f func()) (msg string) {
	defer func() {
		if p := recover(); p != nil {
			s, ok := p.(string)
			if !ok || !strings.HasPrefix(s, "S2-") {
				panic(p)
			}
			msg = s
		}
	}()
	f()
	return ""
}

func TestOnDisk(t *testing.T) {
	e, dir := onDisk(t)
	ctx := context.Background()
	if b, err := os.ReadFile(filepath.Join(dir, "INSTANCE")); err != nil || !strings.Contains(string(b), e.s.instance) {
		t.Fatalf("INSTANCE: %q, %v", b, err)
	}
	id := e.start(e.a, asUser("u1"), uploadSpec("a.txt", SurfaceFirebase))
	if _, err := e.chunk(e.a, "a.txt", id, 0, "hello ", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.chunk(e.a, "a.txt", id, 3, "lo world", false); err != nil {
		t.Fatal(err)
	}
	got, err := e.chunk(e.a, "a.txt", id, 11, "", true)
	if err != nil || got.Object.Size != 11 {
		t.Fatalf("finalize: %+v, %v", got, err)
	}
	_, row, _ := e.s.storedObject(ctx, e.a, "a.txt")
	r, err := e.s.store.Open(row.BlobKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello world" {
		t.Errorf("on disk: %q", b)
	}
	if _, err := e.s.Insert(ctx, e.a, asAdmin(), uploadSpec("a.txt", SurfaceGCS), strings.NewReader("v2"), 2); err != nil {
		t.Fatal(err)
	}
	e.clock.add(2 * time.Minute)
	if n, err := e.s.collectGarbage(ctx); err != nil || n != 1 {
		t.Fatalf("gc: %d, %v", n, err)
	}
	if _, err := e.s.store.Open(row.BlobKey); err == nil {
		t.Error("the replaced file stays after the GC")
	}
	// An orphan left by a crash goes with the next boot's sweep; a second instance waits.
	orphan := blob.ObjectKey(projectA)
	w, err := e.s.store.Create(orphan)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Commit()
	next, err := New(ctx, e.pool, e.s.store, e.opts, slog.New(slog.NewTextHandler(e.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- next.Boot(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("a second instance booted beside the first: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	e.s = next
	if err := next.deleteCandidates(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := next.store.Open(orphan); err == nil {
		t.Error("the orphan stays")
	}
	if e.read(e.a, "a.txt") != "v2" {
		t.Error("the sweep took a live file")
	}
	e.checkUsage(projectA)
}
