package retention

import (
	"context"
	"errors"
	"testing"
)

// Drain stops when a batch comes back short, on an error, or at MaxBatches.
func TestDrain(t *testing.T) {
	ctx := context.Background()
	table := func(rows int64) func(context.Context, int) (int64, error) {
		return func(_ context.Context, batch int) (int64, error) {
			n := min(rows, int64(batch))
			rows -= n
			return n, nil
		}
	}
	for _, c := range []struct {
		rows, want int64
	}{
		{0, 0}, {9, 9}, {10, 10}, {25, 25},
		{10 * MaxBatches, 10 * MaxBatches},
		{10*MaxBatches + 7, 10 * MaxBatches}, // the rest waits for the next pass
	} {
		if got, err := Drain(ctx, 10, table(c.rows)); err != nil || got != c.want {
			t.Errorf("Drain of %d rows = %d, %v; want %d", c.rows, got, err, c.want)
		}
	}
	boom := errors.New("boom")
	calls := 0
	n, err := Drain(ctx, 10, func(context.Context, int) (int64, error) {
		calls++
		if calls == 3 {
			return 0, boom
		}
		return 10, nil
	})
	if !errors.Is(err, boom) || n != 20 || calls != 3 {
		t.Errorf("Drain with a failure = %d, %v after %d calls", n, err, calls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n, err := Drain(cancelled, 10, table(100)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("Drain on a cancelled context = %d, %v", n, err)
	}
	if _, err := Drain(ctx, 0, table(1)); err == nil {
		t.Error("Drain accepted a batch of 0")
	}
	c := Counts{}
	if err := c.Run(ctx, "a.b", 10, table(15)); err != nil || c["a.b"] != 15 || c.Total() != 15 {
		t.Errorf("Run = %v, %v", c, err)
	}
	if err := c.Run(ctx, "c.d", 10, func(context.Context, int) (int64, error) { return 1, boom }); !errors.Is(err, boom) || c["c.d"] != 1 || c.Total() != 16 {
		t.Errorf("Run with a failure = %v, %v", c, err)
	}
}
