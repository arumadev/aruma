package core

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// IndexRead is one snapshot of an index path's read (ADR-025 §1): a read-only REPEATABLE READ
// transaction, so a commit is in all of it or none of it, entries and documents alike, and the
// catalogue it plans with is the one of that snapshot. Index entries hold the latest state alone:
// a read at a read_time, or in a transaction, takes the reference path. Close ends it.
//
// A query holds a snapshot only while it reads a bounded batch, never while it sends results to
// its client (api/indexpath.go), and continues in a later snapshot at the first one's commit:
// there an entry is the first snapshot's when its document did not change since (its
// update_time is at or before the first's read time), and ChangedSince gives the documents that
// did change as they were at the first commit, from the changelog.
type IndexRead struct {
	tx       pgx.Tx
	q        *queries.Queries
	db       DB
	cat      *catalog
	readTime time.Time
	seq      int64
}

// ErrIndexReadTooLong is a query continued in a later snapshot after the changelog retention
// pruned commits it would read: it can no longer answer at its read time. The changelog keeps
// at least MinChangelogRetention, which the index path's batches never approach.
var ErrIndexReadTooLong = &Error{Aborted, "The query ran too long to finish at its read time. Run it again."}

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
	head, err := r.q.IndexReadHead(ctx, queries.IndexReadHeadParams{ProjectNumber: db.Project, DatabaseID: db.ID})
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
	r.cat, r.readTime, r.seq = cached, head.LastTs, head.Seq
	return r, nil
}

// Close ends the read.
func (r *IndexRead) Close(ctx context.Context) { _ = r.tx.Rollback(context.WithoutCancel(ctx)) }

// ReadTime is the commit_time of the commit the read is at.
func (r *IndexRead) ReadTime() time.Time { return r.readTime }

// Seq is the seq of the commit the read is at.
func (r *IndexRead) Seq() int64 { return r.seq }

// CatalogVersion is the version of the read's catalogue: the same in two reads when no index
// commits write changed between them.
func (r *IndexRead) CatalogVersion() int64 { return r.cat.version }

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

// DocumentsWithin returns the documents of the first of paths, in their order, as far as budget
// bytes of them (by value.DocumentSize, as stored) go: the first always, then each while those
// before it hold less than budget. A path without a document has its Path alone and costs
// nothing. The caller asks again for the paths past those returned.
func (r *IndexRead) DocumentsWithin(ctx context.Context, paths []Path, budget int) ([]Document, error) {
	keys := make([][]byte, len(paths))
	for i, p := range paths {
		keys[i] = storedKey(nameKey(p))
	}
	rows, err := r.q.GetDocumentsWithin(ctx, queries.GetDocumentsWithinParams{ProjectNumber: r.db.Project, DatabaseID: r.db.ID,
		Budget: int64(budget), NameKeys: keys})
	if err != nil {
		return nil, err
	}
	out := make([]Document, len(rows))
	for i, x := range rows {
		p := paths[x.Ord-1]
		if x.Name == nil {
			out[i] = Document{Path: p}
			continue
		}
		d, err := docRow{name: x.Name, doc: x.Doc, createTime: *x.CreateTime, updateTime: *x.UpdateTime}.document()
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

// ChangeScope is the documents a query reads: those of the collection CollectionID under Parent,
// or with Group those of every collection of that id in the database.
type ChangeScope struct {
	Parent       Path
	CollectionID string
	Group        bool
}

func (c ChangeScope) holds(p Path) bool {
	return p.CollectionID() == c.CollectionID && (c.Group || slices.Equal(p.Parent(), c.Parent))
}

// ChangedSince returns the documents of scope that the commits after seq, up to the read's,
// changed: each once, as it was before the first of those commits, in name order; one that did
// not exist then has its Path alone. It fails with ErrIndexReadTooLong when the changelog may no
// longer hold every such commit.
func (r *IndexRead) ChangedSince(ctx context.Context, seq int64, scope ChangeScope) ([]Document, error) {
	if r.seq == seq {
		return nil, nil
	}
	floor, err := r.q.ChangelogFloor(ctx, queries.ChangelogFloorParams{ProjectNumber: r.db.Project, DatabaseID: r.db.ID,
		MinSecs: MinChangelogRetention.Seconds()})
	if err != nil {
		return nil, err
	}
	if len(floor) == 0 || floor[0].Seq > seq+1 || floor[0].Seq == seq+1 && !floor[0].Young {
		return nil, ErrIndexReadTooLong
	}
	lo, hi := []byte{value.KeyVersion}, []byte{value.KeyVersion + 1}
	if !scope.Group {
		lo = collectionPrefix(scope.Parent, scope.CollectionID)
		hi = IndexPrefixEnd(lo)
	}
	rows, err := r.q.ChangedSince(ctx, queries.ChangedSinceParams{ProjectNumber: r.db.Project, DatabaseID: r.db.ID,
		AfterSeq: seq, Lo: lo, Hi: hi})
	if err != nil {
		return nil, err
	}
	var out []Document
	for _, x := range rows {
		p, err := pathOfKey(x.Name)
		if err != nil {
			return nil, err
		}
		if !scope.holds(p) {
			continue
		}
		d := Document{Path: p}
		if x.Before != nil {
			if d.Fields, d.CreateTime, d.UpdateTime, err = decodeDocument(x.Before); err != nil {
				return nil, err
			}
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Document) int { return bytes.Compare(nameKey(a.Path), nameKey(b.Path)) })
	return out, nil
}
