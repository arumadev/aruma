// Package retention runs housekeeping's batched deletes: the shape every purge of a table that
// grows with use shares. One statement deletes at most one batch, so a table that grew for years
// is trimmed in short statements, none of which holds a long lock or writes a large transaction,
// and one pass deletes at most MaxBatches batches of a table; the next pass goes on.
package retention

import (
	"context"
	"errors"
	"fmt"
)

// MaxBatches bounds the batches one pass deletes from one table. With a batch of 1000 rows that
// is 50,000 rows a pass, far above what any of these tables gains in an hour.
const MaxBatches = 50

// Drain calls del, which deletes at most batch rows, until it deletes fewer than batch (the table
// is caught up), ctx ends, del fails, or MaxBatches batches ran. It returns the rows deleted.
func Drain(ctx context.Context, batch int, del func(ctx context.Context, batch int) (int64, error)) (int64, error) {
	if batch < 1 {
		return 0, errors.New("retention: the batch must be positive")
	}
	var rows int64
	for range MaxBatches {
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		n, err := del(ctx, batch)
		rows += n
		if err != nil || n < int64(batch) {
			return rows, err
		}
	}
	return rows, nil
}

// Counts records what one pass purged, by table.
type Counts map[string]int64

// Run drains one table into c under its name. A failure keeps what was deleted before it and is
// returned named after the table, so that the caller can go on with the other tables.
func (c Counts) Run(ctx context.Context, table string, batch int, del func(ctx context.Context, batch int) (int64, error)) error {
	n, err := Drain(ctx, batch, del)
	c[table] += n
	if err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}

// Total is the rows purged across tables.
func (c Counts) Total() int64 {
	var n int64
	for _, v := range c {
		n += v
	}
	return n
}
