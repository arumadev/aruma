package core

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// IndexRead is one query's read of index entries and of their documents, all in one snapshot of
// the database (ADR-025 §1): a read-only REPEATABLE READ transaction, so a commit is in all of it
// or none of it, entries and documents alike, and the catalogue it plans with is the one of that
// snapshot. Index entries hold the latest state alone: a read at a read_time, or in a
// transaction, takes the reference path. Close ends it.
type IndexRead struct {
	tx       pgx.Tx
	q        *queries.Queries
	db       DB
	cat      *catalog
	readTime time.Time
}

// BeginIndexRead begins an index read of db at its latest commit.
func (s *PGStore) BeginIndexRead(ctx context.Context, db DB) (*IndexRead, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	cached, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	r := &IndexRead{tx: tx, q: queries.New(tx), db: db}
	head, err := r.q.ReadCatalog(ctx, queries.ReadCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		err = &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	if err == nil && head.CatalogVersion != cached.version {
		// The catalogue changed since it was cached: read the snapshot's own.
		cached, err = readCatalog(ctx, r.q, db)
	}
	if err != nil {
		r.Close(ctx)
		return nil, err
	}
	r.cat, r.readTime = cached, head.LastTs
	return r, nil
}

// Close ends the read.
func (r *IndexRead) Close(ctx context.Context) { _ = r.tx.Rollback(context.WithoutCancel(ctx)) }

// ReadTime is the commit_time of the commit the read is at.
func (r *IndexRead) ReadTime() time.Time { return r.readTime }

// Catalog is the index configuration of the read's snapshot, as IndexCatalog returns it.
func (r *IndexRead) Catalog() *index.Catalog { return r.cat.planner }

// IndexID returns the id of the index ix, normalized, of the read's catalogue: a single-field
// index's (single), or a READY composite's. ok is false for a composite that is not READY.
func (r *IndexRead) IndexID(ix index.Index, single bool) (id int64, ok bool) {
	if single {
		return SingleFieldIndexID(ix.CollectionGroup, ix.Scope, ix.Fields[0].Path, ix.Fields[0].Mode), true
	}
	for _, c := range r.cat.composites {
		if c.ready && !c.vector && c.group == ix.CollectionGroup && c.scope == ix.Scope &&
			slices.EqualFunc(c.fields, ix.Fields, func(f compositeField, g index.IndexField) bool {
				return f.canon == g.Path && f.mode == g.Mode
			}) {
			return c.id, true
		}
	}
	return 0, false
}

// Scan returns the entries of an index in r, ordered by whole key, and how many stored entries
// it read: as ScanIndex, in the read's snapshot.
func (r *IndexRead) Scan(ctx context.Context, ir IndexRange) ([]IndexHit, int, error) {
	if ir.Index > 0 {
		i := slices.IndexFunc(r.cat.composites, func(c *composite) bool { return c.id == ir.Index })
		if i < 0 || !r.cat.composites[i].ready {
			return nil, 0, errIndexNotReady
		}
	}
	return scanEntries(ctx, r.q, r.db, ir)
}

// Documents returns the documents of paths in the read's snapshot, in the order asked; one that
// does not exist has its Path alone.
func (r *IndexRead) Documents(ctx context.Context, paths []Path) ([]Document, error) {
	keys := make([][]byte, len(paths))
	for i, p := range paths {
		keys[i] = storedKey(nameKey(p))
	}
	rows, err := r.q.GetDocuments(ctx, queries.GetDocumentsParams{ProjectNumber: r.db.Project, DatabaseID: r.db.ID, NameKeys: keys})
	if err != nil {
		return nil, err
	}
	found := make(map[string]Document, len(rows))
	for _, x := range rows {
		if x.Name == nil {
			continue
		}
		d, err := docRow{name: x.Name, doc: x.Doc, createTime: *x.CreateTime, updateTime: *x.UpdateTime}.document()
		if err != nil {
			return nil, err
		}
		found[string(x.Name)] = d
	}
	out := make([]Document, len(paths))
	for i, p := range paths {
		d, ok := found[string(nameKey(p))]
		if !ok {
			d = Document{Path: p}
		}
		out[i] = d
	}
	return out, nil
}
