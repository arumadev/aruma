package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/store/storetest"
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
