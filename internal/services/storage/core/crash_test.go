package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A crash at each step of the write order (ADR-022 §5, §6.3) leaves a state that the next
// request, Boot's sweep or the GC repairs: no committed version is lost, no orphan stays, and the
// quota accounts match the rows.

// settle runs what a new process does after its boot: the candidates' sweep, then a GC round
// once gc_grace has passed.
func (e *env) settle() {
	e.t.Helper()
	ctx := context.Background()
	if err := e.s.deleteCandidates(ctx); err != nil {
		e.t.Fatal(err)
	}
	e.clock.add(2 * time.Minute)
	if _, err := e.s.collectGarbage(ctx); err != nil {
		e.t.Fatal(err)
	}
}

// files returns every file the store holds, and rows every key a row points to.
func (e *env) unreferencedFiles() []string {
	e.t.Helper()
	var out []string
	for _, k := range e.store.keys("") {
		if e.count(`SELECT count(*) FROM storage.objects WHERE blob_key = $1`, k)+
			e.count(`SELECT count(*) FROM storage.upload_sessions WHERE staging_key = $1 OR blob_key = $1`, k)+
			e.count(`SELECT count(*) FROM storage.blob_gc WHERE blob_key = $1`, k) == 0 {
			out = append(out, k)
		}
	}
	return out
}

func TestCrashDuringInsert(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		point     string
		committed bool
	}{
		{"insert.written", false}, // the file written, not yet durable
		{"insert.durable", false}, // durable, before the transaction
		{"commit.conflict", false},
		{"commit.before", false}, // the transaction in flight, COMMIT not sent
		{"commit.after", true},   // committed, the reply never sent
	} {
		t.Run(c.point, func(t *testing.T) {
			e := newEnv(t)
			e.insert(e.a, "a.txt", "old")
			crashAt(c.point)
			if !crashed(func() {
				_, _ = e.s.Insert(ctx, e.a, asAdmin(), uploadSpec("a.txt", SurfaceGCS), strings.NewReader("new"), 3)
			}) {
				t.Fatal("no crash")
			}
			e.restart()
			e.settle()
			want := "old"
			if c.committed {
				want = "new"
			}
			if got := e.read(e.a, "a.txt"); got != want {
				t.Errorf("after the crash %q reads, want %q", got, want)
			}
			if files := e.store.keys("p/"); len(files) != 1 {
				t.Errorf("files left: %q", files)
			}
			if n := e.count(`SELECT count(*) FROM storage.blob_gc`); n != 0 {
				t.Errorf("%d rows left in blob_gc after the GC", n)
			}
			e.checkUsage(projectA)
		})
	}
}

// A resumable upload crashed at each of its steps finishes with its next requests: a query for
// the durable offset, the rest from there, and the finalize.
func TestCrashDuringUpload(t *testing.T) {
	ctx := context.Background()
	for _, point := range []string{
		"start.staging",     // the staging file made, the row not written
		"upload.appended",   // appended, not synced
		"upload.synced",     // synced, received not recorded
		"finalize.blob_key", // blob_key written, the file not moved
		"finalize.promoted", // moved, not committed
		"commit.before",     // the commit's transaction in flight
		"commit.after",      // committed, the reply lost
	} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			e.insert(e.a, "a.txt", "old")
			id := ""
			if point != "start.staging" {
				id = e.start(e.a, asUser("u1"), uploadSpec("a.txt", SurfaceFirebase))
				if _, err := e.chunk(e.a, "a.txt", id, 0, "hello ", false); err != nil {
					t.Fatal(err)
				}
			}
			crashAt(point)
			ok := crashed(func() {
				switch point {
				case "start.staging":
					id, _ = e.s.StartUpload(ctx, e.a, asUser("u1"), uploadSpec("a.txt", SurfaceFirebase))
				case "upload.appended", "upload.synced":
					_, _ = e.chunk(e.a, "a.txt", id, 6, "world", false)
				default:
					_, _ = e.chunk(e.a, "a.txt", id, 6, "world", true)
				}
			})
			if !ok {
				t.Fatal("no crash")
			}
			e.restart()
			e.settle()
			if point == "start.staging" {
				if files := e.unreferencedFiles(); len(files) != 0 || len(e.store.keys("s/")) != 0 {
					t.Errorf("the staging file of a start that never wrote its row stays: %q", e.store.keys("s/"))
				}
				e.checkUsage(projectA)
				return
			}
			// The client asks where to resume, sends the rest, and finalizes.
			q, err := e.s.QueryUpload(ctx, e.a, "a.txt", id)
			if err != nil {
				t.Fatal(err)
			}
			if q.Status == UploadActive {
				if q.Received != 6 && q.Received != 11 {
					t.Fatalf("resume at %d", q.Received)
				}
				rest := "hello world"[q.Received:]
				if q, err = e.chunk(e.a, "a.txt", id, q.Received, rest, true); err != nil {
					t.Fatalf("finishing after the crash: %v", err)
				}
			}
			if q.Status != UploadFinal || q.Object == nil || q.Object.Size != 11 {
				t.Fatalf("the session after the crash: %+v", q)
			}
			if got := e.read(e.a, "a.txt"); got != "hello world" {
				t.Errorf("stored %q", got)
			}
			// A repeat answers the same.
			if again, err := e.chunk(e.a, "a.txt", id, 11, "", true); err != nil || again.Object.Generation != q.Object.Generation {
				t.Errorf("a repeat: %+v, %v", again, err)
			}
			e.settle()
			if files := e.unreferencedFiles(); len(files) != 0 {
				t.Errorf("orphans left: %q", files)
			}
			if n := len(e.store.keys("p/")); n != 1 {
				t.Errorf("%d object files left", n)
			}
			e.checkUsage(projectA)
		})
	}
}

// A purge cut in the middle, at each of its steps, finishes on its next run; running it twice is
// nothing; the other project is untouched.
func TestPurgeProject(t *testing.T) {
	ctx := context.Background()
	for _, point := range []string{"", "purge.staging", "purge.files", "cut"} {
		t.Run("crash at "+point, func(t *testing.T) {
			e := newEnv(t)
			for _, b := range []Bucket{e.a, e.b} {
				e.insert(b, "a", "1")
				e.insert(b, "a", "2") // one file in blob_gc
				id := e.start(b, asUser("u1"), uploadSpec("s", SurfaceFirebase))
				if _, err := e.chunk(b, "s", id, 0, "x", false); err != nil {
					t.Fatal(err)
				}
				e.start(b, asUser("u1"), uploadSpec("empty", SurfaceFirebase))
			}
			switch point {
			case "":
			case "cut":
				e.store.purgeCut = 1
				if err := e.s.PurgeProject(ctx, projectA); err == nil {
					t.Fatal("the cut purge succeeded")
				}
			default:
				crashAt(point)
				if !crashed(func() { _ = e.s.PurgeProject(ctx, projectA) }) {
					t.Fatal("no crash")
				}
			}
			for range 2 {
				if err := e.s.PurgeProject(ctx, projectA); err != nil {
					t.Fatal(err)
				}
			}
			for _, table := range []string{"objects", "upload_sessions", "blob_gc", "usage"} {
				if n := e.count(`SELECT count(*) FROM storage.`+table+` WHERE project_number = $1`, projectA); n != 0 {
					t.Errorf("%d rows of A in %s", n, table)
				}
				if n := e.count(`SELECT count(*) FROM storage.`+table+` WHERE project_number = $1`, projectB); n == 0 {
					t.Errorf("B's %s rows went too", table)
				}
			}
			if len(e.store.keys("p/482915736204/"))+len(e.store.keys("s/482915736204/")) != 0 {
				t.Errorf("A's files stay")
			}
			if e.read(e.b, "a") != "2" || len(e.store.keys("s/582915736204/")) != 2 {
				t.Error("B's files went")
			}
			e.checkUsage(projectB)
		})
	}
}
