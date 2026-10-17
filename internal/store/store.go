// Package store owns the database: the connection pool, transactions and migrations.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open connects a pool and verifies the connection.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "aruma"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	return pool, nil
}

// Beginner starts transactions; *pgxpool.Pool satisfies it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn inside a transaction, committing when it returns nil and rolling back otherwise.
// Writes and the events they emit share the transaction, so they commit or vanish together.
func InTx(ctx context.Context, db Beginner, fn func(pgx.Tx) error) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Postgres error codes the service layer maps to API errors.
const (
	codeUniqueViolation = "23505"
	codeCheckViolation  = "23514"
	codeForeignKey      = "23503"
)

// UniqueViolation reports whether err is a unique constraint failure, and on which constraint.
func UniqueViolation(err error) (constraint string, ok bool) {
	return pgCode(err, codeUniqueViolation)
}

// CheckViolation reports whether err is a check constraint failure, and on which constraint.
func CheckViolation(err error) (constraint string, ok bool) {
	return pgCode(err, codeCheckViolation)
}

// ForeignKeyViolation reports whether err is a foreign key failure.
func ForeignKeyViolation(err error) (constraint string, ok bool) {
	return pgCode(err, codeForeignKey)
}

func pgCode(err error, code string) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == code {
		return pgErr.ConstraintName, true
	}
	return "", false
}
