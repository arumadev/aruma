package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// A transaction that holds a row lock decides with the ruleset prepareRules resolved before it
// began, and reads no connection from the pool: with two connections and 200 writers on the
// cold caches of twenty projects, every one finishes. Unprepared, a writer under the lock fails
// at once instead of waiting for a third connection.
func TestDecidingUnderALockNeedsNoConnection(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, storetest.NewDB(t)+"&pool_max_conns=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if pool.Config().MaxConns != 2 {
		t.Fatalf("a pool of %d", pool.Config().MaxConns)
	}
	control := func() *tcore.Service {
		return tcore.New(pool, nil, nil, tcore.Options{ProjectDomain: "aruma.test", PublicURL: "http://127.0.0.1",
			RulesLimits: rules.DefaultLimits()}, slog.New(slog.DiscardHandler))
	}
	tsvc := control()
	owner := &authn.Principal{Kind: authn.KindAccount, AccountID: uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'o@example.com', 'x')`, owner.AccountID); err != nil {
		t.Fatal(err)
	}
	var buckets []Bucket
	for i := range 20 {
		proj, err := tsvc.CreateProject(ctx, owner, tcore.NewProject{ID: fmt.Sprintf("app-%02d-x", i), DisplayName: "x"})
		if err != nil {
			t.Fatal(err)
		}
		rs, _, err := tsvc.CreateRuleset(ctx, owner, proj.ID, rules.Storage, []rules.File{{Name: "storage.rules",
			Content: storageRules("match /{all=**} { allow write: if request.auth.uid == 'alice'; }")}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tsvc.PublishRelease(ctx, owner, proj.ID, "firebase.storage/"+proj.StorageBucket, rs.Name); err != nil {
			t.Fatal(err)
		}
		buckets = append(buckets, Bucket{Project: proj.Number, Name: proj.StorageBucket})
	}
	// cold is a service on a fresh control plane over the same pool: every cache is cold.
	cold := func() *Service {
		return &Service{pool: pool, projects: control(), documents: rules.NoDocuments{}, inflight: rules.NewInflight(64 << 20),
			metrics: NewMetrics(), log: slog.New(slog.DiscardHandler), now: time.Now}
	}
	// write locks the project's row, then decides, as Update, Delete and commitObject decide;
	// locked runs between the two.
	write := func(ctx context.Context, s *Service, b Bucket, prepare bool, locked func()) error {
		if prepare {
			var err error
			if ctx, err = s.prepareRules(ctx, b, alice); err != nil {
				return err
			}
		}
		tx, err := s.begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM control.projects WHERE project_number = $1 FOR UPDATE`, b.Project); err != nil {
			return err
		}
		locked()
		op := ruleOp{Method: rules.MethodDelete, Name: "a.png", Resource: storedObject("a.png")}
		if err := s.authorize(ctx, b, alice, op); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	s := cold()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Go(func() {
			errs <- write(deadline, s, buckets[i%len(buckets)], true, func() { time.Sleep(time.Millisecond) })
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a prepared writer: %v", err)
		}
	}

	// Unprepared, both connections held by locked writers: each fails at once, and neither
	// waits for a third connection.
	s = cold()
	var both sync.WaitGroup
	both.Add(2)
	errs = make(chan error, 2)
	began := time.Now()
	for i := range 2 {
		wg.Go(func() {
			errs <- write(deadline, s, buckets[i], false, func() { both.Done(); both.Wait() })
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("an unprepared writer under the lock: %v, want ErrUnavailable at once", err)
		}
	}
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("the unprepared writers took %v", d)
	}
}
