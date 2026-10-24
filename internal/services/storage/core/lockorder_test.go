package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// Tokens across generations, and the lock order a commit shares with S2-C's writes.

// onLockedRow runs fn in a Storage transaction holding the object's row FOR UPDATE, as S2-C's
// Update and Delete do; fn gets the row, nil when there is none.
func (e *env) onLockedRow(name string, fn func(tx pgx.Tx, q *queries.Queries, row *queries.StorageObject) error) error {
	ctx := context.Background()
	tx, err := e.s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := queries.New(tx)
	row, err := q.LockObject(ctx, queries.LockObjectParams{ProjectNumber: projectA, Name: name})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return tx.Commit(ctx)
	case err != nil:
		return err
	}
	if err := fn(tx, q, &row); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// deleteObject deletes an object as S2-C does: its row, its file to blob_gc and its bytes out of
// the quota, in one transaction on the locked row.
func (e *env) deleteObject(name string) error {
	return e.onLockedRow(name, func(tx pgx.Tx, q *queries.Queries, row *queries.StorageObject) error {
		if err := releaseObject(context.Background(), q, projectA, row.BlobKey, row.Size); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `DELETE FROM storage.objects WHERE project_number = $1 AND name = $2`, projectA, name)
		return err
	})
}

// An overwrite or a recreate never inherits the previous row's download tokens (ADR-022 §9):
// every create over /v0 mints a token of its own, a GCS write carries only the admin's, and the
// token of a deleted object is not one of the object created again under its name.
func TestTokensDoNotCarryOver(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	firebase := func(name string) string {
		o, err := e.s.Insert(ctx, e.a, asAdmin(), uploadSpec(name, SurfaceFirebase), strings.NewReader("x"), 1)
		if err != nil || len(o.DownloadTokens) != 1 {
			t.Fatalf("insert: %+v, %v", o, err)
		}
		return o.DownloadTokens[0]
	}
	tokens := func(name string) []string {
		o, _, err := e.s.storedObject(ctx, e.a, name)
		if err != nil || o == nil {
			t.Fatalf("read %s: %+v, %v", name, o, err)
		}
		return o.DownloadTokens
	}
	first, second := firebase("a"), firebase("a")
	if got := tokens("a"); first == second || !slices.Equal(got, []string{second}) {
		t.Errorf("an overwrite over /v0 holds %q; the first token was %s", got, first)
	}
	e.insert(e.a, "a", "over GCS")
	if got := tokens("a"); got != nil {
		t.Errorf("an overwrite over GCS kept %q", got)
	}
	old := firebase("b")
	if err := e.deleteObject("b"); err != nil {
		t.Fatal(err)
	}
	// The same through an upload session.
	id := e.start(e.a, asUser("u1"), uploadSpec("b", SurfaceFirebase))
	got, err := e.chunk(e.a, "b", id, 0, "again", true)
	if err != nil || slices.Contains(got.Object.DownloadTokens, old) || slices.Contains(tokens("b"), old) || len(tokens("b")) != 1 {
		t.Errorf("the recreated object holds %q, with the deleted one's %s: %v", tokens("b"), old, err)
	}
	e.checkUsage(projectA)
}

// Finalizes race PATCHes and deletes of their name, as S2-C runs them (the row locked FOR UPDATE,
// then the usage account). Every Storage transaction takes the object's row before the account
// and the session's row, one object row at a time, so none deadlocks, and the accounts end
// matching the rows.
func TestCommitRacesUpdateAndDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "hot", "seed")
	patch := func() error {
		return e.onLockedRow("hot", func(tx pgx.Tx, _ *queries.Queries, _ *queries.StorageObject) error {
			time.Sleep(time.Millisecond)
			_, err := tx.Exec(ctx, `UPDATE storage.objects SET metageneration = metageneration + 1, updated = now()
				WHERE project_number = $1 AND name = 'hot'`, projectA)
			return err
		})
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 20 {
		wg.Go(func() {
			id, err := e.s.StartUpload(ctx, e.a, asUser("u1"), uploadSpec("hot", SurfaceFirebase))
			if err == nil {
				_, err = e.s.WriteUpload(ctx, e.a, "hot", id, nil,
					Chunk{Body: strings.NewReader(fmt.Sprint("v", i)), Length: -1, Total: -1, Finalize: true})
			}
			errs <- err
		})
		wg.Go(func() { errs <- patch() })
		wg.Go(func() { errs <- e.deleteObject("hot") })
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the writes are stuck")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	e.checkUsage(projectA)
}
