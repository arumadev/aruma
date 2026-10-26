package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// DefaultLocation is the location_id a database reports when the server sets none. Provisional:
// a self-hosted server has no Google region, and the value is unrecorded beyond production's own
// (europe-west1 for the reference project).
const DefaultLocation = "nam5"

// EnsureDatabase creates a project's '(default)' database and its sequencer row, once: the
// service calls it when Documents is enabled for a project, and every call that finds no
// '(default)' does too, so a project created later needs no step of its own (ADR-024 §1.2).
func (s *PGStore) EnsureDatabase(ctx context.Context, project int64) error {
	etag := make([]byte, 12)
	_, _ = rand.Read(etag)
	return s.q.EnsureDatabase(ctx, queries.EnsureDatabaseParams{
		ProjectNumber: project, DatabaseID: DefaultDatabase, Uid: uuid.New(),
		Etag: base64.RawURLEncoding.EncodeToString(etag), LocationID: s.location,
	})
}

// Database implements Store.
func (s *PGStore) Database(ctx context.Context, db DB) (Database, error) {
	if err := s.serving(); err != nil {
		return Database{}, err
	}
	for first := true; ; first = false {
		row, err := s.q.GetDatabase(ctx, queries.GetDatabaseParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			if !first {
				return Database{}, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
			}
			if err := s.missing(ctx, db); err != nil {
				return Database{}, err
			}
			continue
		}
		if err != nil {
			return Database{}, err
		}
		var retention time.Duration
		if r := row.VersionRetention; r.Valid {
			retention = time.Duration(r.Microseconds)*time.Microsecond +
				time.Duration(r.Days)*24*time.Hour + time.Duration(r.Months)*30*24*time.Hour
		}
		return Database{
			DB: db, UID: row.Uid.String(), CreateTime: row.CreateTime, UpdateTime: row.UpdateTime, Etag: row.Etag,
			Type: row.Type, Edition: row.Edition, LocationID: row.LocationID, ConcurrencyMode: row.ConcurrencyMode,
			DeleteProtection: row.DeleteProtection, PointInTimeRecovery: row.PointInTimeRecovery,
			VersionRetention: retention, IndexEnforcement: deref(row.IndexEnforcement),
		}, nil
	}
}

// EarliestVersionTime is the oldest read_time the database serves.
func (d Database) EarliestVersionTime(now time.Time) time.Time { return now.Add(-d.VersionRetention) }

// missing handles a call that found no database: '(default)' is created, and the call runs
// again; any other database does not exist (named databases come in v0.4.x, ADR-028 §3).
func (s *PGStore) missing(ctx context.Context, db DB) error {
	if db.ID != DefaultDatabase {
		return &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	return s.EnsureDatabase(ctx, db.Project)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
