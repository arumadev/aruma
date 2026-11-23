package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// A request that meets a database that went away is UNAVAILABLE, which the SDKs retry, and not
// INTERNAL, which they do not: an operator's pg_terminate_backend (57P01), a Postgres that shuts
// down (57P03), a refused connection, and pgx's "prepare" wrapping of the same. Errors that are
// the server's own stay INTERNAL.
func TestFailMapsALostDatabaseToUnavailable(t *testing.T) {
	s := New(Options{Logger: slog.New(slog.DiscardHandler)})
	ctx := context.Background()
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	for _, err := range []error{
		&pgconn.PgError{Severity: "FATAL", Code: "57P01", Message: "terminating connection due to administrator command"},
		fmt.Errorf("error preprocessing batch (prepare): %w", &pgconn.PgError{Severity: "FATAL", Code: "57P01"}),
		&pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is shutting down"},
		fmt.Errorf("failed to connect to `user=postgres database=x`: %w", refused),
	} {
		got := wire.From(s.fail(ctx, err))
		if got.Code != wire.CodeUnavailable {
			t.Errorf("%v: %v, want UNAVAILABLE", err, got)
		}
	}
	for _, err := range []error{
		errors.New("a bug"),
		&pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: "relation does not exist"},
	} {
		if got := wire.From(s.fail(ctx, err)); got.Code != wire.CodeInternal {
			t.Errorf("%v: %v, want INTERNAL", err, got)
		}
	}
	// The core's own answer to a lost COMMIT keeps its text.
	if got := wire.From(s.fail(ctx, core.ErrUnknownOutcome)); got.Code != wire.CodeUnavailable || got.Message != core.ErrUnknownOutcome.Message {
		t.Errorf("ErrUnknownOutcome: %v", got)
	}
}
