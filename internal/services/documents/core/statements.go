package core

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// statement returns the SQL and arguments of one generated query, without running it: call runs
// the generated method on a Queries that only records. The SQL stays in queries/*.sql, checked by
// sqlc against the schema and by the scope test; core runs it where the generated method cannot,
// in a pgx batch (the commit path), or scans it itself where sqlc's types do not hold the
// statement's NULLs (a LEFT JOIN LATERAL's columns).
func statement(call func(q *queries.Queries) error) (string, []any) {
	var r recorder
	if err := call(queries.New(&r)); !errors.Is(err, errRecorded) || r.sql == "" {
		panic("documents: a generated query did not reach the recorder")
	}
	return r.sql, r.args
}

var errRecorded = errors.New("recorded")

// recorder is a queries.DBTX that keeps the statement it is given and fails it.
type recorder struct {
	sql  string
	args []any
}

func (r *recorder) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = sql, args
	return pgconn.CommandTag{}, errRecorded
}

func (r *recorder) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = sql, args
	return nil, errRecorded
}

func (r *recorder) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	r.sql, r.args = sql, args
	return failedRow{}
}

type failedRow struct{}

func (failedRow) Scan(...any) error { return errRecorded }
