package core

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Document Database tries for its lock again at least every few seconds by default after a
// lost session: until it does it answers UNAVAILABLE, and with a 30 s ceiling a 15 s Postgres
// restart kept it down 16 s after Postgres was back (the perf agent's outage runs).
func TestInstanceRetriesWithinSecondsByDefault(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	i := StartInstance(context.Background(), cfg, InstanceOptions{BootWait: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer i.Close()
	if i.o.MaxBackoff > 5*time.Second || i.o.MaxBackoff != DefaultInstanceMaxBackoff {
		t.Errorf("MaxBackoff %v, want DefaultInstanceMaxBackoff (%v), at most 5s", i.o.MaxBackoff, DefaultInstanceMaxBackoff)
	}
}
