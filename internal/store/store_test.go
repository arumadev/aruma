package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// AC-2: an unmigrated database is refused, and checking it runs no DDL.
func TestCheckCurrentRefusesUnmigratedWithoutDDL(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.NewEmptyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := store.CheckCurrent(ctx, pool); !errors.Is(err, store.ErrSchemaOutdated) {
		t.Fatalf("CheckCurrent = %v, want ErrSchemaOutdated", err)
	}
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("checking the schema created %d relations", n)
	}
}

// AC-3: running migrations twice is a no-op the second time.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewEmptyDB(t)
	for i := 0; i < 2; i++ {
		if err := store.Migrate(ctx, url); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := store.CheckCurrent(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

// markAhead records a migration of schema that this binary does not have, as a newer aruma
// would, and returns its version.
func markAhead(t *testing.T, pool *pgxpool.Pool, schema string) int64 {
	t.Helper()
	ctx := context.Background()
	statuses, err := store.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.Schema == schema {
			v := s.Latest + 1
			if _, err := pool.Exec(ctx, "INSERT INTO public.goose_"+schema+" (version_id, is_applied) VALUES ($1, true)", v); err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no schema %s", schema)
	return 0
}

func TestSchemaStatusStates(t *testing.T) {
	cases := []struct {
		s              store.SchemaStatus
		pending, ahead bool
	}{
		{store.SchemaStatus{Current: 2, Latest: 3}, true, false},
		{store.SchemaStatus{Current: 3, Latest: 3}, false, false},
		{store.SchemaStatus{Current: 4, Latest: 3}, false, true},
	}
	for _, c := range cases {
		if c.s.Pending() != c.pending || c.s.Ahead() != c.ahead {
			t.Errorf("%+v: Pending %v Ahead %v, want %v %v", c.s, c.s.Pending(), c.s.Ahead(), c.pending, c.ahead)
		}
	}
}

// The server refuses a schema a newer aruma migrated, as it refuses an outdated one.
func TestCheckCurrentRefusesNewerSchema(t *testing.T) {
	pool := storetest.NewPool(t)
	v := markAhead(t, pool, "auth")
	err := store.CheckCurrent(context.Background(), pool)
	if !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("CheckCurrent = %v, want ErrSchemaNewer", err)
	}
	if want := fmt.Sprintf("(auth at %d of %d)", v, v-1); !strings.Contains(err.Error(), want) {
		t.Fatalf("CheckCurrent = %q, want it to name %s", err, want)
	}
}

// goose applies nothing and reports success on a database ahead of its files, so Migrate
// refuses before goose runs at all. Control's last migration is unrecorded here, so any goose
// run would act on it; no version may move.
func TestMigrateRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewDB(t)
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	markAhead(t, pool, "auth")
	if _, err := pool.Exec(ctx, "DELETE FROM public.goose_control WHERE version_id = (SELECT max(version_id) FROM public.goose_control)"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx, url); !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("Migrate = %v, want ErrSchemaNewer", err)
	}
	after, err := store.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatalf("Migrate changed the versions: %+v, then %+v", before, after)
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	boom := errors.New("boom")
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO events.changes (project_number, service, type, subject)
			VALUES (100000000001, 'test', 'created', 'x')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM events.changes").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rolled-back insert left %d rows", n)
	}
}
