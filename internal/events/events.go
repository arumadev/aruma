// Package events writes the shared change log, events.changes.
//
// A change is appended inside the same transaction as the write it describes, so it commits
// or rolls back with it: readers never see an event for a change that did not happen, nor miss
// one that did. Phase 1 only creates the log; Auth (Phase 2) is its first writer.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Change is one entry of the log.
type Change struct {
	Seq           int64
	ProjectNumber int64
	Service       string // e.g. "auth", "firestore"
	Type          string // e.g. "user.created"
	Subject       string // the changed entity, e.g. a user id
	Data          json.RawMessage
	CommittedAt   time.Time
}

// Append adds a change to the log within tx.
func Append(ctx context.Context, tx pgx.Tx, c Change) (int64, error) {
	if c.ProjectNumber == 0 || c.Service == "" || c.Type == "" || c.Subject == "" {
		return 0, errors.New("events: project, service, type and subject are required")
	}
	data := c.Data
	if data == nil {
		data = json.RawMessage(`{}`)
	}
	var seq int64
	err := tx.QueryRow(ctx, `INSERT INTO events.changes (project_number, service, type, subject, data)
		VALUES ($1, $2, $3, $4, $5) RETURNING seq`, c.ProjectNumber, c.Service, c.Type, c.Subject, data).Scan(&seq)
	return seq, err
}

// Querier runs read queries; *pgxpool.Pool and pgx.Tx satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Since returns up to limit changes of one project after seq, oldest first.
func Since(ctx context.Context, q Querier, projectNumber, seq int64, limit int) ([]Change, error) {
	rows, err := q.Query(ctx, `SELECT seq, project_number, service, type, subject, data, committed_at
		FROM events.changes WHERE project_number = $1 AND seq > $2 ORDER BY seq LIMIT $3`, projectNumber, seq, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Change, error) {
		var c Change
		err := r.Scan(&c.Seq, &c.ProjectNumber, &c.Service, &c.Type, &c.Subject, &c.Data, &c.CommittedAt)
		return c, err
	})
}
