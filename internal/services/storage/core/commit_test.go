package core

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"hash/crc32"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// commitObject and the write order around it (ADR-022 §5), through Insert and Rewrite.

func TestInsert(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := "hello, storage"
	o := e.insert(e.a, "dir/a.txt", body)
	crc := crc32.Checksum([]byte(body), crc32.MakeTable(crc32.Castagnoli))
	if o.Size != int64(len(body)) || o.CRC32C != crc || o.MD5 != md5.Sum([]byte(body)) || o.Generation == 0 ||
		o.Metageneration != 1 || o.ContentType != defaultContentType || o.ContentDisposition != "" || o.DownloadTokens != nil {
		t.Fatalf("Insert = %+v", o)
	}
	if got := e.read(e.a, "dir/a.txt"); got != body {
		t.Errorf("stored %q", got)
	}
	// Firebase REST stores its default disposition and a new download token (ADR-022 §9, §10.4).
	f, err := e.s.Insert(ctx, e.a, admin(), UploadSpec{Name: "b.png", Surface: SurfaceFirebase, Declared: -1,
		Attrs: Patch{Fields: map[string]*string{FieldContentType: strPtr("image/png")}, Metadata: map[string]*string{"k": strPtr("v")}}},
		strings.NewReader("png"), -1)
	if err != nil || len(f.DownloadTokens) != 1 || f.ContentType != "image/png" || f.ContentDisposition != "inline; filename*=utf-8''b.png" ||
		f.Metadata["k"] != "v" {
		t.Fatalf("a Firebase insert: %+v, %v", f, err)
	}
	stored, _, err := e.s.storedObject(ctx, e.a, "b.png")
	if err != nil || !slices.Equal(stored.DownloadTokens, f.DownloadTokens) {
		t.Errorf("the token as stored: %+v, %v", stored, err)
	}
	if n := e.count(`SELECT count(*) FROM storage.objects WHERE download_tokens::text LIKE '%' || $1 || '%'`, f.DownloadTokens[0]); n != 0 {
		t.Error("a download token is stored in the clear")
	}
	used, reserved, _, objects := e.usage(projectA)
	if used != int64(len(body)+3) || reserved != 0 || objects != 2 {
		t.Errorf("usage %d used, %d reserved, %d objects", used, reserved, objects)
	}
	e.checkUsage(projectA)
}

// An overwrite replaces the row under its lock and queues the file it replaced, once.
func TestInsertOverwrite(t *testing.T) {
	e := newEnv(t)
	first := e.insert(e.a, "a.txt", "one")
	_, row1, _ := e.s.storedObject(context.Background(), e.a, "a.txt")
	second := e.insert(e.a, "a.txt", "second")
	if second.Generation <= first.Generation || second.Created.Before(first.Created) {
		t.Errorf("generations %d then %d", first.Generation, second.Generation)
	}
	if e.read(e.a, "a.txt") != "second" {
		t.Error("the overwrite is not what reads")
	}
	if n := e.count(`SELECT count(*) FROM storage.blob_gc WHERE blob_key = $1`, row1.BlobKey); n != 1 {
		t.Errorf("the replaced file is queued %d times", n)
	}
	if !e.store.has(row1.BlobKey) {
		t.Error("the replaced file was deleted inline, before gc_grace")
	}
	e.checkUsage(projectA)
}

// Concurrent overwrites of one name: each waits on the lock of the one before, so every replaced
// key reaches blob_gc exactly once, the last write stands, and no file leaks.
func TestCommitConcurrentOverwrites(t *testing.T) {
	e := newEnv(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	objs := make([]Object, n)
	for i := range n {
		wg.Go(func() {
			objs[i], errs[i] = e.s.Insert(context.Background(), e.a, admin(), spec("hot.txt", SurfaceGCS),
				strings.NewReader(fmt.Sprint("version ", i)), -1)
		})
	}
	wg.Wait()
	gens := map[int64]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		gens[objs[i].Generation] = true
	}
	if len(gens) != n {
		t.Errorf("%d distinct generations for %d writes", len(gens), n)
	}
	files := e.store.keys("p/")
	rows, err := e.pool.Query(context.Background(), `SELECT blob_key FROM storage.blob_gc`)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	_, cur, _ := e.s.storedObject(context.Background(), e.a, "hot.txt")
	all := append(slices.Clone(queued), cur.BlobKey)
	slices.Sort(all)
	slices.Sort(files)
	if len(queued) != n-1 || !slices.Equal(all, files) || slices.Contains(queued, cur.BlobKey) {
		t.Errorf("blob_gc %d keys %q and the current %s; the files are %q", len(queued), queued, cur.BlobKey, files)
	}
	e.checkUsage(projectA)
}

// A delete that commits between the insert's conflict and the lock leaves no row to lock: the
// write inserts again and succeeds, never 412.
func TestCommitRetriesAfterConcurrentDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a.txt", "one")
	deletes := 0
	testHook = func(p string) {
		if p == "commit.conflict" && deletes < 2 {
			deletes++
			if _, err := e.pool.Exec(ctx, `DELETE FROM storage.objects WHERE project_number = $1 AND name = 'a.txt'`, projectA); err != nil {
				t.Error(err)
			}
		}
	}
	o, err := e.s.Insert(ctx, e.a, admin(), spec("a.txt", SurfaceGCS), strings.NewReader("two"), 3)
	if err != nil || deletes != 1 || e.read(e.a, "a.txt") != "two" || o.Metageneration != 1 {
		t.Fatalf("Insert = %+v, %v after %d deletes", o, err, deletes)
	}
	// A delete racing every write: none answers 412.
	testHook = nil
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := e.pool.Exec(ctx, `DELETE FROM storage.objects WHERE project_number = $1 AND name = 'race.txt'`, projectA); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for i := range 40 {
		if _, err := e.s.Insert(ctx, e.a, admin(), spec("race.txt", SurfaceGCS), strings.NewReader(fmt.Sprint(i)), -1); err != nil {
			t.Errorf("write %d racing a delete: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// ifGenerationMatch=0 takes the DO NOTHING branch alone: on an existing name it is 412 and
// nothing changes, and two creates at once do not both succeed.
func TestIfGenerationMatchZero(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	zero := int64(0)
	create := func(name, body string) (Object, error) {
		sp := spec(name, SurfaceGCS)
		sp.Pre.IfGenerationMatch = &zero
		return e.s.Insert(ctx, e.a, admin(), sp, strings.NewReader(body), int64(len(body)))
	}
	if _, err := create("a.txt", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := create("a.txt", "second"); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("a create over an existing name: %v", err)
	}
	if e.read(e.a, "a.txt") != "first" || len(e.store.keys("p/")) != 1 {
		t.Errorf("the refused create changed something: %q", e.store.keys("p/"))
	}
	var wg sync.WaitGroup
	var won, lost int
	var mu sync.Mutex
	for i := range 8 {
		wg.Go(func() {
			_, err := create("race.txt", fmt.Sprint(i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrPreconditionFailed):
				lost++
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if won != 1 || lost != 7 {
		t.Errorf("%d creates won and %d lost", won, lost)
	}
	e.checkUsage(projectA)
}

func TestPreconditions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	o := e.insert(e.a, "a.txt", "x")
	i := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		name string
		obj  string
		pre  Preconditions
		ok   bool
	}{
		{"generation matches", "a.txt", Preconditions{IfGenerationMatch: i(o.Generation)}, true},
		{"generation differs", "a.txt", Preconditions{IfGenerationMatch: i(o.Generation + 1)}, false},
		{"generation not match, differs", "a.txt", Preconditions{IfGenerationNotMatch: i(1)}, true},
		{"metageneration differs", "a.txt", Preconditions{IfMetagenerationMatch: i(7)}, false},
		{"metageneration not match, same", "a.txt", Preconditions{IfMetagenerationNotMatch: i(1)}, false},
		{"generation match on a missing object", "none.txt", Preconditions{IfGenerationMatch: i(5)}, false},
		{"generation not match on a missing object", "none.txt", Preconditions{IfGenerationNotMatch: i(5)}, false},
		{"metageneration match on a missing object", "none.txt", Preconditions{IfMetagenerationMatch: i(1)}, false},
	} {
		sp := spec(c.obj, SurfaceGCS)
		sp.Pre = c.pre
		// The clock stands still here, so each overwrite is the previous generation + 1.
		cur, _, _ := e.s.storedObject(ctx, e.a, "a.txt")
		if c.pre.IfGenerationMatch != nil && c.obj == "a.txt" {
			sp.Pre.IfGenerationMatch = i(cur.Generation + *c.pre.IfGenerationMatch - o.Generation)
		}
		_, err := e.s.Insert(ctx, e.a, admin(), sp, strings.NewReader("y"), 1)
		if c.ok != (err == nil) || (!c.ok && !errors.Is(err, ErrPreconditionFailed)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if n := len(e.store.keys("p/")); n != int(e.count(`SELECT count(*) FROM storage.objects`)+e.count(`SELECT count(*) FROM storage.blob_gc`)) {
		t.Errorf("a refused write left its file: %d files", n)
	}
	e.checkUsage(projectA)
}

// A refusal before COMMIT deletes the new file at once and gives the reservation back.
func TestInsertRefusalsLeaveNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wrong := uint32(1)
	for _, c := range []struct {
		name string
		sp   UploadSpec
		body string
		len  int64
		hook func(context.Context, Bucket, Principal, ruleOp) error
		want error
	}{
		{"rules", spec("a.txt", SurfaceFirebase), "abc", 3, func(context.Context, Bucket, Principal, ruleOp) error { return ErrPermissionDenied }, ErrPermissionDenied},
		{"overloaded rules", spec("a.txt", SurfaceFirebase), "abc", 3, func(context.Context, Bucket, Principal, ruleOp) error { return ErrOverloaded }, ErrOverloaded},
		{"crc32c", UploadSpec{Name: "a.txt", Surface: SurfaceGCS, Declared: -1, Want: Checksums{CRC32C: &wrong}}, "abc", -1, nil, ErrChecksumMismatch},
		{"declared size", UploadSpec{Name: "a.txt", Surface: SurfaceGCS, Declared: 5}, "abc", -1, nil, ErrSizeMismatch},
		{"over the declared size", UploadSpec{Name: "a.txt", Surface: SurfaceGCS, Declared: 2}, "abc", -1, nil, ErrSizeMismatch},
		{"short body", spec("a.txt", SurfaceGCS), "abc", 5, nil, errShortBody},
	} {
		e.s.authorizeHook = c.hook
		_, err := e.s.Insert(ctx, e.a, user("u1"), c.sp, strings.NewReader(c.body), c.len)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
		if keys := e.store.keys(""); len(keys) != 0 {
			t.Errorf("%s: files left: %q", c.name, keys)
		}
		if _, reserved, _, _ := e.usage(projectA); reserved != 0 {
			t.Errorf("%s: %d bytes still reserved", c.name, reserved)
		}
	}
	e.s.authorizeHook = nil
	// Before S2-E, the contract refuses everyone but admins.
	if _, err := e.s.Insert(ctx, e.a, user("u1"), spec("a.txt", SurfaceFirebase), strings.NewReader("x"), 1); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("a user without rules: %v", err)
	}
	if _, err := e.s.Insert(ctx, e.a, admin(), spec("a.txt", SurfaceFirebase), strings.NewReader("x"), 1); err != nil {
		t.Errorf("an admin without rules: %v", err)
	}
	lim := e.s.limits
	lim.ObjectBytes = 4
	e.s.limits = lim
	if _, err := e.s.Insert(ctx, e.a, admin(), spec("big", SurfaceGCS), strings.NewReader("12345"), -1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over object_bytes: %v", err)
	}
	if _, err := e.s.Insert(ctx, e.a, admin(), spec("big", SurfaceGCS), strings.NewReader("12345"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a stated length over object_bytes: %v", err)
	}
	e.checkUsage(projectA)
}

// The rules see the incoming object complete, and the existing one; an overwrite is a create.
func TestInsertRulesOp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a.txt", "old")
	var op ruleOp
	var who Principal
	e.s.authorizeHook = func(_ context.Context, _ Bucket, p Principal, o ruleOp) error { op, who = o, p; return nil }
	if _, err := e.s.Insert(ctx, e.a, user("u1"), spec("a.txt", SurfaceFirebase), strings.NewReader("new!"), -1); err != nil {
		t.Fatal(err)
	}
	if op.Method.String() != "create" || op.Resource == nil || op.Resource.Size != 3 || op.Incoming == nil ||
		op.Incoming.Size != 4 || op.Incoming.MD5 != md5.Sum([]byte("new!")) || op.Surface != SurfaceFirebase || who.UID != "u1" {
		t.Errorf("the rules saw %+v as %+v", op, who)
	}
}

// The ambiguous COMMIT (ADR-022 §5): whatever happened to the reply, no committed version is lost.
func TestAmbiguousCommit(t *testing.T) {
	ctx := context.Background()
	t.Run("the reply is lost after the commit", func(t *testing.T) {
		e := newEnv(t)
		commitTx = func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return errors.New("connection reset by peer")
		}
		o, err := e.s.Insert(ctx, e.a, admin(), spec("a.txt", SurfaceGCS), strings.NewReader("kept"), 4)
		if err != nil || o.Size != 4 || e.read(e.a, "a.txt") != "kept" {
			t.Fatalf("Insert = %+v, %v", o, err)
		}
		if v := metricValue(t, e.s.metrics.AmbiguousCommits, "committed"); v != 1 {
			t.Errorf("settled committed %v times", v)
		}
		e.checkUsage(projectA)
	})
	t.Run("the backend is killed before COMMIT", func(t *testing.T) {
		e := newEnv(t)
		commitTx = func(ctx context.Context, tx pgx.Tx) error {
			var pid int
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			if _, err := e.pool.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		_, err := e.s.Insert(ctx, e.a, admin(), spec("a.txt", SurfaceGCS), strings.NewReader("gone"), 4)
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, errCommitUnknown) {
			t.Fatalf("Insert = %v, want an aborted commit", err)
		}
		if v := metricValue(t, e.s.metrics.AmbiguousCommits, "aborted"); v != 1 {
			t.Errorf("settled aborted %v times", v)
		}
		// Aborted: nothing is visible, so the new file went at once.
		if keys := e.store.keys(""); len(keys) != 0 || e.count(`SELECT count(*) FROM storage.objects`) != 0 {
			t.Errorf("after an aborted commit: files %q", keys)
		}
		e.checkUsage(projectA)
	})
	t.Run("the request is cancelled during COMMIT", func(t *testing.T) {
		e := newEnv(t)
		rctx, cancel := context.WithCancel(ctx)
		commitTx = func(ctx context.Context, tx pgx.Tx) error {
			cancel() // the client hangs up as COMMIT goes out
			return tx.Commit(ctx)
		}
		o, err := e.s.Insert(rctx, e.a, admin(), spec("a.txt", SurfaceGCS), strings.NewReader("stays"), 5)
		if err != nil || e.read(e.a, "a.txt") != "stays" || o.Size != 5 {
			t.Fatalf("a commit cancelled by its client: %+v, %v", o, err)
		}
	})
	t.Run("the outcome cannot be settled", func(t *testing.T) {
		e := newEnv(t)
		old := e.insert(e.a, "a.txt", "old")
		commitTx = func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return errors.New("i/o timeout")
		}
		xactStatusOf = func(context.Context, string) (string, error) { return "", errors.New("connection refused") }
		_, err := e.s.Insert(ctx, e.a, admin(), spec("a.txt", SurfaceGCS), strings.NewReader("new"), 3)
		if !errors.Is(err, errCommitUnknown) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Insert = %v, want an unknown outcome", err)
		}
		// It did commit: the new version reads, its file stayed, and the old one is queued.
		if e.read(e.a, "a.txt") != "new" {
			t.Fatal("the committed version was lost")
		}
		cur, _, _ := e.s.storedObject(ctx, e.a, "a.txt")
		if cur.Generation <= old.Generation {
			t.Error("the old version reads")
		}
		// The next boot's sweep keeps it: a row points to it.
		commitTx = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
		xactStatusOf = nil
		s := e.restart()
		if err := s.deleteCandidates(ctx); err != nil {
			t.Fatal(err)
		}
		if e.read(e.a, "a.txt") != "new" {
			t.Error("the sweep deleted a committed file")
		}
		e.checkUsage(projectA)
	})
}

// Rewrite copies within a project, counted on the quota, with the tokens sealed again for the
// destination's name unless the metadata is replaced.
func TestRewrite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src, err := e.s.Insert(ctx, e.a, admin(), UploadSpec{Name: "src.txt", Surface: SurfaceFirebase, Declared: -1,
		Attrs: Patch{Fields: map[string]*string{FieldCacheControl: strPtr("no-cache")}, Metadata: map[string]*string{"k": strPtr("v")}}},
		strings.NewReader("payload"), -1)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "dst.txt", admin(), nil, Preconditions{})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Size != src.Size || cp.CRC32C != src.CRC32C || cp.MD5 != src.MD5 || cp.CacheControl != "no-cache" ||
		cp.Metadata["k"] != "v" || !slices.Equal(cp.DownloadTokens, src.DownloadTokens) || e.read(e.a, "dst.txt") != "payload" {
		t.Errorf("Rewrite = %+v", cp)
	}
	stored, _, err := e.s.storedObject(ctx, e.a, "dst.txt")
	if err != nil || !slices.Equal(stored.DownloadTokens, src.DownloadTokens) {
		t.Errorf("the destination's tokens did not open for its name: %+v, %v", stored, err)
	}
	// New metadata replaces the source's, tokens included.
	cp, err = e.s.Rewrite(ctx, e.a, "src.txt", e.a, "dst2.txt", admin(), &Patch{Fields: map[string]*string{FieldContentType: strPtr("text/plain")}}, Preconditions{})
	if err != nil || cp.ContentType != "text/plain" || cp.CacheControl != "" || cp.Metadata != nil || cp.DownloadTokens != nil {
		t.Errorf("Rewrite with metadata = %+v, %v", cp, err)
	}
	if _, err := e.s.Rewrite(ctx, e.a, "none", e.a, "x", admin(), nil, Preconditions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing source: %v", err)
	}
	if _, err := e.s.Rewrite(ctx, e.a, "src.txt", e.b, "x", admin(), nil, Preconditions{}); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("into another project: %v", err)
	}
	zero := int64(0)
	if _, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "dst.txt", admin(), nil, Preconditions{IfGenerationMatch: &zero}); !errors.Is(err, ErrPreconditionFailed) {
		t.Errorf("ifGenerationMatch=0 on an existing destination: %v", err)
	}
	// The quota counts the copy.
	lim := e.s.limits
	lim.ProjectBytes = e.count(`SELECT used_bytes FROM storage.usage WHERE project_number = $1`, projectA) + 3
	lim.ObjectBytes = 3
	e.s.limits = lim
	if _, err := e.s.Rewrite(ctx, e.a, "src.txt", e.a, "over.txt", admin(), nil, Preconditions{}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("a copy over the quota: %v", err)
	}
	if e.store.has("") || len(e.store.keys("p/")) != int(e.count(`SELECT count(*) FROM storage.objects`)+e.count(`SELECT count(*) FROM storage.blob_gc`)) {
		t.Error("a refused copy left its file")
	}
	e.checkUsage(projectA)
}

func TestCheckPreconditionsWithoutRow(t *testing.T) {
	zero, one := int64(0), int64(1)
	if checkRowPreconditions(Preconditions{IfGenerationMatch: &zero}, nil) != nil || checkRowPreconditions(Preconditions{}, nil) != nil {
		t.Error("a create's preconditions on no row")
	}
	row := &queries.StorageObject{Generation: 1, Metageneration: 1}
	if checkRowPreconditions(Preconditions{IfGenerationMatch: &zero}, row) == nil || checkRowPreconditions(Preconditions{IfGenerationNotMatch: &one}, row) == nil {
		t.Error("preconditions that fail on the row pass")
	}
}

func strPtr(s string) *string { return &s }
