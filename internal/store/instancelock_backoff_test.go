package store

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A process that lost the lock tries for it again at least every few seconds by default: the
// service answers 503 until it does, and with a 30 s ceiling a 15 s Postgres restart kept the
// Realtime Database down 16 s after Postgres was back (the perf agent's outage runs).
func TestInstanceLockRetriesWithinSecondsByDefault(t *testing.T) {
	cfg, err := pgx.ParseConfig("postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	l := StartInstanceLock(context.Background(), cfg, InstanceLockOptions{Name: "test.backoff", BootWait: time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer l.Close()
	if l.o.MaxBackoff > 5*time.Second || l.o.MaxBackoff != DefaultInstanceMaxBackoff {
		t.Errorf("MaxBackoff %v, want DefaultInstanceMaxBackoff (%v), at most 5s", l.o.MaxBackoff, DefaultInstanceMaxBackoff)
	}
}
