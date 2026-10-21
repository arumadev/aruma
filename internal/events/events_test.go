package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/events"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// AC-30: an event appended in a transaction that fails leaves no trace; one in a committed
// transaction is visible, in order, to its project only.
func TestEventsFollowTheirTransaction(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	change := events.Change{ProjectNumber: 482915736204, Service: "auth", Type: "user.created", Subject: "u1"}

	failed := errors.New("write failed")
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := events.Append(ctx, tx, change); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if got, _ := events.Since(ctx, pool, change.ProjectNumber, 0, 10); len(got) != 0 {
		t.Fatalf("rolled-back event visible: %+v", got)
	}

	for _, subject := range []string{"u2", "u3"} {
		c := change
		c.Subject = subject
		if err := store.InTx(ctx, pool, func(tx pgx.Tx) error { _, err := events.Append(ctx, tx, c); return err }); err != nil {
			t.Fatal(err)
		}
	}
	other := change
	other.ProjectNumber = 582915736204
	if err := store.InTx(ctx, pool, func(tx pgx.Tx) error { _, err := events.Append(ctx, tx, other); return err }); err != nil {
		t.Fatal(err)
	}

	got, err := events.Since(ctx, pool, change.ProjectNumber, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Subject != "u2" || got[1].Subject != "u3" || got[0].Seq >= got[1].Seq {
		t.Fatalf("events: %+v", got)
	}
	if string(got[0].Data) != "{}" {
		t.Fatalf("default data: %s", got[0].Data)
	}
	if rest, _ := events.Since(ctx, pool, change.ProjectNumber, got[0].Seq, 10); len(rest) != 1 {
		t.Fatalf("since seq: %+v", rest)
	}
}

func TestAppendValidates(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := events.Append(ctx, tx, events.Change{Service: "auth"})
		return err
	})
	if err == nil {
		t.Fatal("incomplete change accepted")
	}
}
