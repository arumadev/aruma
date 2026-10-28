package core

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

func countRows(t *testing.T, pool *pgxpool.Pool, table string, project int64) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM documents."+table+" WHERE project_number = $1", project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Retention deletes the versions that ended before the read_time window and the changelog rows
// older than its retention, in bounded batches, and keeps the rest (ADR-024 §8, review M4).
func TestRetention(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	for i := range 10 {
		mustCommit(t, s, set("c/d", doc("n", value.Int(int64(i)))))
	}
	if v, c := countRows(t, pool, "versions", testDB.Project), countRows(t, pool, "changelog", testDB.Project); v != 9 || c != 10 {
		t.Fatalf("before: %d versions, %d changelog rows", v, c)
	}
	o := RetentionOptions{Changelog: time.Hour, Batch: 2, MaxBatches: 2, Log: slog.New(slog.DiscardHandler)}

	// Nothing is old yet.
	if n, err := s.Prune(ctx, o); err != nil || n != (Pruned{}) {
		t.Fatalf("Prune of recent rows = %+v, %v", n, err)
	}

	// The six oldest versions and changelog rows age past their windows.
	for _, q := range []string{
		`UPDATE documents.versions SET begin_time = begin_time - interval '3 hours', end_time = end_time - interval '3 hours'
			WHERE project_number = $1 AND end_seq <= 7`,
		`UPDATE documents.changelog SET commit_time = commit_time - interval '3 hours' WHERE project_number = $1 AND seq <= 6`,
	} {
		if _, err := pool.Exec(ctx, q, testDB.Project); err != nil {
			t.Fatal(err)
		}
	}
	// A pass deletes at most MaxBatches × Batch of each.
	if n, err := s.Prune(ctx, o); err != nil || n != (Pruned{Versions: 4, Changelog: 4}) {
		t.Fatalf("first pass = %+v, %v", n, err)
	}
	if n, err := s.Prune(ctx, o); err != nil || n != (Pruned{Versions: 2, Changelog: 2}) {
		t.Fatalf("second pass = %+v, %v", n, err)
	}
	if n, err := s.Prune(ctx, o); err != nil || n != (Pruned{}) {
		t.Fatalf("third pass = %+v, %v", n, err)
	}
	var minSeq int64
	if err := pool.QueryRow(ctx, "SELECT min(seq) FROM documents.changelog WHERE project_number = $1", testDB.Project).Scan(&minSeq); err != nil || minSeq != 7 {
		t.Fatalf("oldest changelog seq %d: %v", minSeq, err)
	}
	if v := countRows(t, pool, "versions", testDB.Project); v != 3 {
		t.Fatalf("%d versions left", v)
	}

	// The document still reads.
	got, _, err := s.Get(ctx, testDB, ParsePath("c/d"), ReadOptions{})
	if err != nil || !got.Exists() {
		t.Fatalf("Get after retention: %v", err)
	}
}

// Retention runs only on the process that holds the single-instance lock.
func TestRetentionNeedsTheLock(t *testing.T) {
	pool := storetest.NewPool(t)
	other := StartInstance(t.Context(), pool.Config().ConnConfig, InstanceOptions{BootWait: 2 * time.Second, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(other.Close)
	if !other.Held() {
		t.Fatal("the first instance does not hold the lock")
	}
	mine := StartInstance(t.Context(), pool.Config().ConnConfig, InstanceOptions{BootWait: 10 * time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(mine.Close)
	if err := NewPGStore(pool, Options{}).EnsureDatabase(t.Context(), testDB.Project); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPGStore(pool, Options{Instance: mine}).Prune(t.Context(), RetentionOptions{}); !errors.Is(err, ErrNotServing) {
		t.Fatalf("Prune without the lock = %v", err)
	}
	if _, err := NewPGStore(pool, Options{Instance: other}).Prune(t.Context(), RetentionOptions{}); err != nil {
		t.Fatalf("Prune with the lock = %v", err)
	}
}

// PurgeProject removes every row of the project, and none of another's (review M5).
func TestPurgeProject(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	other := DB{Project: 1, ProjectID: "other-1", ID: DefaultDatabase}
	for i := range 3 {
		mustCommit(t, s, set("c/d", doc("n", value.Int(int64(i)))), set("c/d/s/e", doc("n", value.Int(int64(i)))))
	}
	if _, err := s.Commit(ctx, other, []Write{set("c/d", doc("n", value.Int(1)))}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO documents.usage (project_number, database_id, day, writes) VALUES ($1, '(default)', current_date, 6)`, testDB.Project); err != nil {
		t.Fatal(err)
	}

	for range 2 { // idempotent
		if err := PurgeProject(ctx, pool, nil, testDB.Project); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"documents", "versions", "changelog", "index_entries", "usage", "sequencer", "databases"} {
		if n := countRows(t, pool, table, testDB.Project); n != 0 {
			t.Errorf("%d rows left in %s", n, table)
		}
	}
	if n := countRows(t, pool, "documents", other.Project); n != 1 {
		t.Errorf("the other project has %d documents", n)
	}
}
