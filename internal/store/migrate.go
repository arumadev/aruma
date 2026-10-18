package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/osama300/aurma-project/internal/store/migrations"
)

// SchemaStatus is the migration state of one schema.
type SchemaStatus struct {
	Schema  string
	Current int64
	Latest  int64
}

// Pending reports whether migrations are waiting to be applied.
func (s SchemaStatus) Pending() bool { return s.Current < s.Latest }

// ErrSchemaOutdated is returned when the database is behind the binary's migrations.
var ErrSchemaOutdated = errors.New("database schema is outdated: run `aruma migrate up`")

// Migrate applies every pending migration, schema by schema. Only the operator command calls
// it; the server never runs DDL (ADR-009).
func Migrate(ctx context.Context, databaseURL string) error {
	// Parse as the server does, so pool settings such as pool_max_conns are understood rather
	// than sent to Postgres as runtime parameters.
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*cfg.ConnConfig)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	for _, schema := range migrations.Schemas {
		p, err := provider(db, schema)
		if err != nil {
			return err
		}
		if _, err := p.Up(ctx); err != nil {
			return fmt.Errorf("migrate %s: %w", schema, err)
		}
	}
	return nil
}

// Status reads the applied and latest version of each schema without writing anything,
// so the server can call it at startup.
func Status(ctx context.Context, conn Querier) ([]SchemaStatus, error) {
	out := make([]SchemaStatus, 0, len(migrations.Schemas))
	for _, schema := range migrations.Schemas {
		latest, err := latestVersion(schema)
		if err != nil {
			return nil, err
		}
		current, err := appliedVersion(ctx, conn, schema)
		if err != nil {
			return nil, fmt.Errorf("status %s: %w", schema, err)
		}
		out = append(out, SchemaStatus{Schema: schema, Current: current, Latest: latest})
	}
	return out, nil
}

// CheckCurrent returns ErrSchemaOutdated unless every schema is fully migrated.
func CheckCurrent(ctx context.Context, conn Querier) error {
	statuses, err := Status(ctx, conn)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		if s.Pending() {
			return fmt.Errorf("%w (%s at %d of %d)", ErrSchemaOutdated, s.Schema, s.Current, s.Latest)
		}
	}
	return nil
}

func versionTable(schema string) string { return "public.goose_" + schema }

func appliedVersion(ctx context.Context, conn Querier, schema string) (int64, error) {
	table := versionTable(schema)
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	// The table name comes from the fixed schema list, never from input.
	var v int64
	err := conn.QueryRow(ctx, "SELECT coalesce(max(version_id), 0) FROM "+table+" WHERE is_applied").Scan(&v)
	return v, err
}

func latestVersion(schema string) (int64, error) {
	names, err := fs.Glob(migrations.FS, schema+"/*.sql")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, name := range names {
		v, err := goose.NumericComponent(name)
		if err != nil {
			return 0, fmt.Errorf("migration %s: %w", name, err)
		}
		latest = max(latest, v)
	}
	return latest, nil
}

// provider builds a goose provider for one schema, with its own version table so each schema
// is numbered independently.
func provider(db *sql.DB, schema string) (*goose.Provider, error) {
	fsys, err := fs.Sub(migrations.FS, schema)
	if err != nil {
		return nil, err
	}
	st, err := database.NewStore(database.DialectPostgres, versionTable(schema))
	if err != nil {
		return nil, err
	}
	return goose.NewProvider("", db, fsys, goose.WithStore(st))
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
