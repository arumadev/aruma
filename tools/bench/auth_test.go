package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/store/storetest"
)

func TestSpreadAddrCyclesThroughThePool(t *testing.T) {
	defer func(n uint64) { clientAddrs = n }(clientAddrs)
	addrCounter.Store(0)
	clientAddrs = 3
	got := []string{spreadAddr(), spreadAddr(), spreadAddr(), spreadAddr()}
	if got[0] != "10.0.0.1" || got[2] != "10.0.0.3" || got[3] != got[0] {
		t.Fatalf("pool of 3: %v", got)
	}
	clientAddrs = 0
	if a, b := spreadAddr(), spreadAddr(); a == b {
		t.Fatalf("unbounded: %s twice", a)
	}
}

func TestParallelRunsEveryIndexAndStopsAtTheFirstError(t *testing.T) {
	var seen [100]atomic.Bool
	if err := parallel(t.Context(), 100, 4, func(_ context.Context, i int) error { seen[i].Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	for i := range seen {
		if !seen[i].Load() {
			t.Fatalf("index %d never ran", i)
		}
	}
	// The calls past the failing one wait for the stop, so however the workers are scheduled, no
	// worker is free to start another index before index 10 has failed: 0-10 and at most one
	// waiting call on each of the other three workers.
	boom := errors.New("boom")
	var ran atomic.Int64
	err := parallel(t.Context(), 1000, 4, func(ctx context.Context, i int) error {
		ran.Add(1)
		switch {
		case i == 10:
			return boom
		case i > 10:
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	if !errors.Is(err, boom) || ran.Load() > 14 {
		t.Fatalf("err %v after %d calls", err, ran.Load())
	}
}

// TestAuthScenariosAgainstServer seeds the Auth fixture on an in-process server and sends one
// request of each Auth scenario: each must succeed, or the bench would measure errors.
func TestAuthScenariosAgainstServer(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Postgres; skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if c, err := pgx.Connect(ctx, storetest.AdminURL()); err != nil {
		if os.Getenv("ARUMA_TEST_REQUIRE_DB") != "" {
			t.Fatal(err)
		}
		t.Skipf("no test Postgres: %v", err)
	} else {
		c.Close(ctx)
	}
	target, err := StartInProcess(ctx, io.Discard, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	hc := &http.Client{Timeout: 30 * time.Second}
	fx, err := Seed(ctx, hc, target, 1, Needs{SA: true, Auth: true, AuthUsers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if a := fx.Auth; len(a.IDTokens) != 4 || len(a.RefreshTokens) != 4 || len(a.CustomTokens) != customTokenUsers {
		t.Fatalf("fixture: %d ID tokens, %d refresh tokens, %d custom tokens", len(a.IDTokens), len(a.RefreshTokens), len(a.CustomTokens))
	}
	for _, sc := range authScenarios {
		do := requester(hc, sc, sc.New(target, fx))
		for range 2 { // twice: a sign-up must not reuse its email
			if out := do(ctx, 0); out.Err != nil || out.Status != http.StatusOK {
				t.Errorf("%s: status %d, error %v", sc.Name, out.Status, out.Err)
			}
		}
	}
}
