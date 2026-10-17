// Package storetest gives tests a fresh, fully migrated Postgres database.
//
// Each call clones a template database that is migrated once per migration set, so a new
// database costs one CREATE DATABASE. Databases are dropped when the test ends. No Docker:
// tests run against the local Postgres named by ARUMA_TEST_DATABASE_URL.
package storetest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/store/migrations"
)

// DefaultAdminURL is used when ARUMA_TEST_DATABASE_URL is unset.
const DefaultAdminURL = "postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable"

// lockKey serialises template creation across concurrently running test binaries.
const lockKey = 0x41_72_75_6d_61 // "Aruma"

// AdminURL returns the URL of a database the tests may use to create others.
func AdminURL() string {
	if u := os.Getenv("ARUMA_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return DefaultAdminURL
}

// NewDB returns the URL of a fresh, migrated database.
func NewDB(t testing.TB) string {
	t.Helper()
	return newDB(t, true)
}

// NewEmptyDB returns the URL of a fresh database with no migrations applied.
func NewEmptyDB(t testing.TB) string {
	t.Helper()
	return newDB(t, false)
}

// NewPool returns a pool on a fresh, migrated database.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := store.Open(context.Background(), NewDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newDB(t testing.TB, migrated bool) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Postgres; skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, AdminURL())
	if err != nil {
		if os.Getenv("ARUMA_TEST_REQUIRE_DB") != "" {
			t.Fatalf("connect to test Postgres: %v", err)
		}
		t.Skipf("no test Postgres at ARUMA_TEST_DATABASE_URL: %v", err)
	}
	defer admin.Close(context.Background())

	name := "aruma_t_" + randomHex(6)
	create := "CREATE DATABASE " + name
	if migrated {
		tmpl, err := ensureTemplate(ctx, admin)
		if err != nil {
			t.Fatal(err)
		}
		create += " TEMPLATE " + tmpl
	}
	if _, err := admin.Exec(ctx, create); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, AdminURL())
		if err != nil {
			t.Logf("drop %s: %v", name, err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop %s: %v", name, err)
		}
	})
	return withDatabase(AdminURL(), name)
}

// ensureTemplate creates the migrated template for the current migration set if needed.
func ensureTemplate(ctx context.Context, admin *pgx.Conn) (string, error) {
	tmpl := "aruma_tmpl_" + migrationsHash()
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return "", err
	}
	defer admin.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey)

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", tmpl).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return tmpl, nil
	}
	// Templates of earlier migration sets are stale once a new one is needed.
	rows, err := admin.Query(ctx, "SELECT datname FROM pg_database WHERE datname LIKE 'aruma\\_tmpl\\_%'")
	if err != nil {
		return "", err
	}
	stale, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	for _, name := range stale {
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	}
	building := tmpl + "_build"
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)"); err != nil {
		return "", err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+building); err != nil {
		return "", err
	}
	if err := store.Migrate(ctx, withDatabase(AdminURL(), building)); err != nil {
		return "", fmt.Errorf("migrate template: %w", err)
	}
	// Renaming last means a half-built template is never used.
	if _, err := admin.Exec(ctx, "ALTER DATABASE "+building+" RENAME TO "+tmpl); err != nil {
		return "", err
	}
	return tmpl, nil
}

func migrationsHash() string {
	h := sha256.New()
	_ = fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(migrations.FS, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func withDatabase(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
