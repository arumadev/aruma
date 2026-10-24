package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// The contract's database parts (S2-0), on Postgres: the transaction helper, releaseObject, and
// the commit statements S2-D builds commitObject from.

const testProject = 482915736204

func dbService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	pool := storetest.NewPool(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO control.accounts (id, email, password_hash) VALUES ('00000000-0000-7000-8000-000000000001', 'o@example.com', 'x')`,
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (482915736204, 'chat-app', 'Chat', '00000000-0000-7000-8000-000000000001', '1_a', 'aruma.test')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{pool: pool, reads: queries.New(pool), keys: testKeys(t), log: slog.New(slog.DiscardHandler)}, pool
}

// ADR-022 §5: a Storage transaction commits synchronously on a server whose default is off, and
// the setting stays inside it: the pool keeps the server's default for every other service.
func TestInTxCommitsSynchronously(t *testing.T) {
	s, pool := dbService(t)
	ctx := context.Background()
	var name string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+" SET synchronous_commit = off"); err != nil {
		t.Fatal(err)
	}
	pool.Reset()
	show := func(q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}) string {
		var v string
		if err := q.QueryRow(ctx, "SHOW synchronous_commit").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := show(pool); v != "off" {
		t.Fatalf("the database's default is %q", v)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v := show(tx); v != "on" {
		t.Errorf("inside begin: synchronous_commit = %q", v)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(ctx, func(q *queries.Queries) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if v := show(pool); v != "off" {
		t.Errorf("after a Storage transaction, the pool's setting is %q", v)
	}
	// An error rolls back.
	boom := errors.New("boom")
	err = s.inTx(ctx, func(q *queries.Queries) error {
		if err := q.QueueBlobGC(ctx, queries.QueueBlobGCParams{BlobKey: blob.ObjectKey(testProject), ProjectNumber: testProject}); err != nil {
			return err
		}
		return boom
	})
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM storage.blob_gc").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(err, boom) || n != 0 {
		t.Errorf("inTx = %v with %d rows queued; want the error and none", err, n)
	}
}

func TestWarnUnsafeDurability(t *testing.T) {
	s, _ := dbService(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := s.warnUnsafeDurability(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The test server runs with fsync on, as every server must.
	if strings.Contains(logs.String(), "fsync off") {
		t.Errorf("a warning on a server with fsync on: %s", logs.String())
	}
}

// releaseObject takes an object's bytes and count out of the quota and queues its file.
func TestReleaseObject(t *testing.T) {
	s, pool := dbService(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO storage.usage (project_number, used_bytes, object_count) VALUES ($1, 100, 2)`, testProject); err != nil {
		t.Fatal(err)
	}
	key := blob.ObjectKey(testProject)
	if err := s.inTx(ctx, func(q *queries.Queries) error { return releaseObject(ctx, q, testProject, key, 40) }); err != nil {
		t.Fatal(err)
	}
	u, err := s.reads.GetUsage(ctx, testProject)
	if err != nil || u.UsedBytes != 60 || u.ObjectCount != 1 {
		t.Errorf("usage after the release: %+v, %v", u, err)
	}
	var queued string
	if err := pool.QueryRow(ctx, "SELECT blob_key FROM storage.blob_gc").Scan(&queued); err != nil || queued != key {
		t.Errorf("blob_gc holds %q, %v; want %q", queued, err, key)
	}
	// The same file twice is a bug, refused by the outbox's key.
	if err := s.inTx(ctx, func(q *queries.Queries) error { return releaseObject(ctx, q, testProject, key, 1) }); err == nil {
		t.Error("a file is queued twice")
	}
}

// write is commitObject's row step as the statements allow it (ADR-022 §5): insert, or on a
// conflict lock the row and replace it, releasing what it replaced. hold, when set, runs with
// the row locked, before the update. ifGenerationMatch=0 is the insert alone.
func write(ctx context.Context, s *Service, key string, mustNotExist bool, hold func()) (queries.StorageObject, error) {
	var out queries.StorageObject
	err := s.inTx(ctx, func(q *queries.Queries) error {
		md5 := make([]byte, 16)
		now := time.Now()
		o, err := q.InsertObject(ctx, queries.InsertObjectParams{
			ProjectNumber: testProject, Name: "a.png", Generation: now.UnixMicro(), BlobKey: key, Size: 10, Md5: md5,
			Metadata: json.RawMessage(`{}`), Now: now,
		})
		switch {
		case err == nil:
			out = o
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		case mustNotExist:
			return ErrPreconditionFailed
		}
		old, err := q.LockObject(ctx, queries.LockObjectParams{ProjectNumber: testProject, Name: "a.png"})
		if err != nil {
			return err
		}
		if hold != nil {
			hold()
		}
		o, err = q.ReplaceObject(ctx, queries.ReplaceObjectParams{
			ProjectNumber: testProject, Name: "a.png", Generation: now.UnixMicro(), BlobKey: key, Size: 10, Md5: md5,
			Metadata: json.RawMessage(`{}`), Now: now,
		})
		if err != nil {
			return err
		}
		out = o
		return releaseObject(ctx, q, testProject, old.BlobKey, old.Size)
	})
	return out, err
}

// Two overwrites of one name at once: the second waits on the first's lock, then reads its
// committed row, so each replaced key goes to blob_gc exactly once and no file leaks.
func TestCommitStatementsUnderConcurrentOverwrites(t *testing.T) {
	s, pool := dbService(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO storage.usage (project_number, used_bytes, object_count) VALUES ($1, 1000, 3)`, testProject); err != nil {
		t.Fatal(err)
	}
	k0, k1, k2 := blob.ObjectKey(testProject), blob.ObjectKey(testProject), blob.ObjectKey(testProject)
	first, err := write(ctx, s, k0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	var r1, r2 queries.StorageObject
	var e1, e2 error
	wg.Go(func() {
		r1, e1 = write(ctx, s, k1, false, func() { close(locked); <-release })
	})
	<-locked
	wg.Go(func() { r2, e2 = write(ctx, s, k2, false, nil) })
	time.Sleep(200 * time.Millisecond) // the second writer is now waiting on the lock
	close(release)
	wg.Wait()
	if e1 != nil || e2 != nil {
		t.Fatalf("writes: %v, %v", e1, e2)
	}
	cur, err := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: testProject, Name: "a.png"})
	if err != nil || cur.BlobKey != k2 {
		t.Fatalf("the row points to %q, %v; want the last writer's", cur.BlobKey, err)
	}
	if !(first.Generation < r1.Generation && r1.Generation < r2.Generation) {
		t.Errorf("generations %d, %d, %d do not increase", first.Generation, r1.Generation, r2.Generation)
	}
	rows, err := pool.Query(ctx, "SELECT blob_key FROM storage.blob_gc ORDER BY queued_at, blob_key")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := pgx.CollectRows(rows, pgx.RowTo[string])
	slices.Sort(queued)
	want := []string{k0, k1}
	slices.Sort(want)
	if err != nil || !slices.Equal(queued, want) {
		t.Errorf("blob_gc = %q, want the two replaced keys %q", queued, want)
	}
	// Each overwrite released what it replaced, once; counting the new generation is S2-D's.
	if u, err := s.reads.GetUsage(ctx, testProject); err != nil || u.UsedBytes != 980 || u.ObjectCount != 1 {
		t.Errorf("usage %+v, %v; want 980 bytes and 1 object", u, err)
	}
}

// ifGenerationMatch=0 twice at once on a new name: the second insert waits for the first and
// then does nothing, so exactly one create succeeds.
func TestCommitStatementsCreateRace(t *testing.T) {
	s, pool := dbService(t)
	ctx := context.Background()
	tx, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	params := func(key string) queries.InsertObjectParams {
		return queries.InsertObjectParams{ProjectNumber: testProject, Name: "new.png", Generation: now.UnixMicro(), BlobKey: key,
			Md5: make([]byte, 16), Metadata: json.RawMessage(`{}`), Now: now}
	}
	if _, err := queries.New(tx).InsertObject(ctx, params(blob.ObjectKey(testProject))); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.inTx(ctx, func(q *queries.Queries) error {
			_, err := q.InsertObject(ctx, params(blob.ObjectKey(testProject)))
			return err
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("the second create did not wait for the first: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("the second create = %v, want no row (412)", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM storage.objects").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d objects, %v", n, err)
	}
}

// A delete that commits between InsertObject's conflict and LockObject leaves no row to lock:
// the write retries the insert, which then succeeds, and never answers 412 for it.
func TestCommitStatementsRetryAfterConcurrentDelete(t *testing.T) {
	s, pool := dbService(t)
	ctx := context.Background()
	now := time.Now()
	params := func(key string) queries.InsertObjectParams {
		return queries.InsertObjectParams{ProjectNumber: testProject, Name: "a.png", Generation: now.UnixMicro(), BlobKey: key,
			Md5: make([]byte, 16), Metadata: json.RawMessage(`{}`), Now: now}
	}
	if err := s.inTx(ctx, func(q *queries.Queries) error {
		_, err := q.InsertObject(ctx, params(blob.ObjectKey(testProject)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	key := blob.ObjectKey(testProject)
	var tries int
	err := s.inTx(ctx, func(q *queries.Queries) error {
		for tries = 1; tries <= 3; tries++ {
			_, err := q.InsertObject(ctx, params(key))
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if tries == 1 {
				// Another request deletes the object now, and commits.
				if _, err := pool.Exec(ctx, "DELETE FROM storage.objects WHERE project_number = $1", testProject); err != nil {
					return err
				}
			}
			_, err = q.LockObject(ctx, queries.LockObjectParams{ProjectNumber: testProject, Name: "a.png"})
			if errors.Is(err, pgx.ErrNoRows) {
				continue // deleted in between: insert again
			}
			return err
		}
		return errors.New("no attempt succeeded")
	})
	if err != nil || tries != 2 {
		t.Fatalf("write = %v after %d tries; want success on the second", err, tries)
	}
	cur, err := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: testProject, Name: "a.png"})
	if err != nil || cur.BlobKey != key {
		t.Errorf("the row points to %q, %v; want the retried write's", cur.BlobKey, err)
	}
}
