package tokens

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osama300/aurma-project/internal/services/auth/queries"
)

// countingDB counts the queries sent through it.
type countingDB struct {
	queries.DBTX
	n      atomic.Int64
	during func() // runs inside each Query, before it returns
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n.Add(1)
	if c.during != nil {
		c.during()
	}
	return c.DBTX.Query(ctx, sql, args...)
}

func (c *countingDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n.Add(1)
	return c.DBTX.QueryRow(ctx, sql, args...)
}

func (c *countingDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.n.Add(1)
	return c.DBTX.Exec(ctx, sql, args...)
}

// Verification reads the project's keys once, then answers from memory until a key changes or
// keyCacheTTL passes. A rotation in this process is seen at once.
func TestVerifyCachesPublishedKeys(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	db := &countingDB{DBTX: m.pool}
	m.q = queries.New(db)
	tok, err := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	base := db.n.Load()
	for i := 0; i < 100; i++ {
		if _, err := m.Verify(ctx, projA, IDToken, tok); err != nil {
			t.Fatal(err)
		}
	}
	if n := db.n.Load() - base; n != 1 {
		t.Fatalf("100 verifications ran %d queries, want 1", n)
	}
	// An unknown kid is refused from the cache too: it cannot become valid without a key
	// change, which would clear the cache.
	forged := otherKid(t)
	if _, err := m.Verify(ctx, projA, IDToken, forged); !errors.Is(err, ErrSignature) {
		t.Fatalf("unknown kid: %v", err)
	}
	if n := db.n.Load() - base; n != 1 {
		t.Fatalf("an unknown kid went to the database (%d queries)", n)
	}

	if err := m.Rotate(ctx, projA, IDToken, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, projA, IDToken, tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("token of a key destroyed in an emergency rotation: %v", err)
	}

	fresh, _ := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	m.Verify(ctx, projA, IDToken, fresh)
	base = db.n.Load()
	m.now = func() time.Time { return time.Now().Add(keyCacheTTL + time.Second) }
	if _, err := m.Verify(ctx, projA, IDToken, fresh); err != nil {
		t.Fatal(err)
	}
	if n := db.n.Load() - base; n != 1 {
		t.Fatalf("after the TTL: %d queries, want a reload", n)
	}
}

// A load that overlaps a key change is not kept, so it cannot pin the keys from before it.
func TestKeyLoadRacingAChangeIsNotKept(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	db := &countingDB{DBTX: m.pool}
	db.during = func() { m.keysChanged(projA, IDToken) } // a rotation commits mid-load
	m.q = queries.New(db)
	if _, err := m.publishedKeys(ctx, projA, IDToken); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, kept := m.published[signerKey{projA, IDToken}]
	m.mu.Unlock()
	if kept {
		t.Fatal("a load that raced a key change was cached")
	}
}

func otherKid(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := sign(strings.Repeat("0", 40), key, testClaims{Sub: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	return forged
}
