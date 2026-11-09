package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// Replicas started together (init containers, a rolling deploy) each run `aruma migrate up`.
// Without a lock they raced on the same DDL, and some failed with a duplicate key error from
// the Postgres catalog ("pg_namespace_nspname_index") although the schema was fine.
func TestConcurrentMigratorsAllSucceed(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewEmptyDB(t)
	const migrators = 6
	errs := make([]error, migrators)
	var wg sync.WaitGroup
	for i := range migrators {
		wg.Go(func() { errs[i] = store.Migrate(ctx, url) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v", i, err)
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
