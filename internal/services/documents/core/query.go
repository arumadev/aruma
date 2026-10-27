package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// Queries is the queries' part of the store (ADR-025 §1): the candidates the reference path
// reads, and the index catalogue enforcement checks queries against.
type Queries interface {
	// Scan returns a page of a query's candidates in name order: the documents of a collection,
	// of every collection directly under the parent (kindless), of every collection of one id
	// below the parent (a collection group), or every document below the parent (kindless,
	// all descendants). In a transaction it reads as the transaction does and takes the lock of
	// the scope it reads: the collection; the collection group's id, database-wide; or the
	// database for a kindless scan (ADR-025 §7.3).
	Scan(ctx context.Context, db DB, r ScanRequest) (ListResult, error)
	// IndexCatalog returns a database's declared composite indexes and field overrides.
	IndexCatalog(ctx context.Context, db DB) (*index.Catalog, error)
	// BeginIndexRead begins the planner's read of index entries and their documents at the
	// latest commit, in one snapshot (indexread.go).
	BeginIndexRead(ctx context.Context, db DB) (*IndexRead, error)
}

// ScanRequest is a page of a query's candidates.
type ScanRequest struct {
	// Parent is the document whose collections are read; nil for the root.
	Parent Path
	// CollectionID is the collection read; "" reads every collection (kindless).
	CollectionID string
	// AllDescendants reads the collections anywhere below Parent, not only directly under it.
	AllDescendants bool
	// From starts the page past that document (ListResult.Next of the page before), or at it
	// with FromInclusive: any document path of the database, which need not be in the scope
	// read. A kindless scan directly under Parent starts past From only when From is one of its
	// documents, and from its first document otherwise.
	From          Path
	FromInclusive bool
	// PageSize is the most documents returned; 0 is MaxPageSize.
	PageSize int
	ReadOptions
}

// lock is the scope a scan reads, for a transaction's lock (ADR-024 §6.1): a collection, the
// collection group's id database-wide, or the database for a kindless scan.
func (r ScanRequest) lock() string {
	switch {
	case r.CollectionID == "":
		return databaseLock
	case r.AllDescendants:
		h := sha256.Sum256([]byte(r.CollectionID))
		return "g" + string(h[:])
	}
	h := sha256.Sum256(collectionPrefix(r.Parent, r.CollectionID))
	return "c" + string(h[:])
}

// after is the whole name key the page starts past: From's key, or for an inclusive From the
// key without its final 0x00, which is above every key below From's and at or below From's
// own and its descendants'. nil starts at the first.
func (r ScanRequest) after() []byte {
	if r.From == nil {
		return nil
	}
	k := nameKey(r.From)
	if r.FromInclusive {
		k = k[:len(k)-1]
	}
	return k
}

// Scan implements Queries.
func (s *PGStore) Scan(ctx context.Context, db DB, r ScanRequest) (ListResult, error) {
	if r.Transaction != nil {
		return s.txnScan(ctx, db, r)
	}
	n := pageSize(r.PageSize)
	var docs []Document
	var readTime time.Time
	var err error
	switch {
	case r.CollectionID != "" && !r.AllDescendants:
		docs, readTime, err = s.collectionPage(ctx, db, r.Parent, r.CollectionID, r.after(), n+1, r.ReadTime)
	case r.CollectionID == "" && !r.AllDescendants:
		lr := ListRequest{Parent: r.Parent, ReadOptions: r.ReadOptions}
		if lr.After = r.From; r.FromInclusive || !lr.after() {
			lr.After = nil
		}
		docs, readTime, err = s.listAll(ctx, db, lr, n+1)
	default:
		docs, readTime, err = s.descendants(ctx, db, r, n+1)
	}
	if err != nil {
		return ListResult{}, err
	}
	res := ListResult{Documents: docs, ReadTime: readTime}
	if len(docs) > n {
		res.Documents = docs[:n]
		res.Next = docs[n-1].Path
	}
	return res, nil
}

// descendants returns up to limit documents below r.Parent past r's start, in name order: of
// every collection, or of the collection id r names. They are one range of name keys: the
// parent's prefix and a segment (ADR-024 §1.3), which the parent's own key is below.
func (s *PGStore) descendants(ctx context.Context, db DB, r ScanRequest, limit int) ([]Document, time.Time, error) {
	prefix := prefixKey(r.Parent)
	lo := lowerBound(append(bytes.Clone(prefix), segNumeric))
	hi := upperBound(append(bytes.Clone(prefix), pastChildren))
	after := r.after()
	if after != nil && bytes.Compare(lowerBound(after), lo) > 0 {
		lo = lowerBound(after)
	}
	if after == nil {
		after = []byte{}
	}
	var cg []byte
	if r.CollectionID != "" {
		h := sha256.Sum256([]byte(r.CollectionID))
		cg = h[:]
	}
	h, rows, err := s.read(ctx, db, r.ReadTime, func(t time.Time) (string, []any) {
		return statement(func(q *queries.Queries) error {
			var err error
			switch {
			case cg == nil && t.IsZero():
				_, err = q.ScanRange(ctx, queries.ScanRangeParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					Lo: lo, Hi: hi, After: after, RowLimit: int32(limit)})
			case cg == nil:
				_, err = q.ScanRangeAt(ctx, queries.ScanRangeAtParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					Lo: lo, Hi: hi, After: after, RowLimit: int32(limit), ReadTime: t})
			case t.IsZero():
				_, err = q.ScanGroup(ctx, queries.ScanGroupParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					CgHash: cg, CollectionID: r.CollectionID, Lo: lo, Hi: hi, After: after, RowLimit: int32(limit)})
			default:
				_, err = q.ScanGroupAt(ctx, queries.ScanGroupAtParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					CgHash: cg, CollectionID: r.CollectionID, Lo: lo, Hi: hi, After: after, RowLimit: int32(limit), ReadTime: t})
			}
			return err
		})
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	// A row is below the parent when its whole name continues the parent's prefix with a
	// segment: a cut key may pass the stored bounds and not be.
	below := func(name []byte) bool {
		return bytes.HasPrefix(name, prefix) && len(name) > len(prefix) && name[len(prefix)] != keyEnd &&
			bytes.Compare(name, after) > 0
	}
	// As in collectionPage: a full page ending in a run of keys cut alike reads the rest of the
	// run, then everything is ordered by the whole name.
	if len(rows) == limit && len(rows[limit-1].name) > cutLen {
		lo, hi := chainBounds(rows[limit-1].name)
		run, err := s.rangeAt(ctx, db, lo, hi, h.readTime)
		if err != nil {
			return nil, time.Time{}, err
		}
		seen := map[string]bool{}
		for _, x := range rows {
			seen[string(x.name)] = true
		}
		for _, x := range run {
			if !seen[string(x.name)] {
				rows = append(rows, x)
			}
		}
	}
	slices.SortFunc(rows, func(a, b docRow) int { return bytes.Compare(a.name, b.name) })
	docs := make([]Document, 0, min(len(rows), limit))
	for _, x := range rows {
		if len(docs) == limit {
			break
		}
		if !below(x.name) {
			continue
		}
		d, err := x.document()
		if err != nil {
			return nil, time.Time{}, err
		}
		if r.CollectionID != "" && d.Path.CollectionID() != r.CollectionID {
			continue // a row of the run read by name key alone
		}
		docs = append(docs, d)
	}
	return docs, h.readTime, nil
}

// txnScan is Scan in a transaction (ScanRequest.Transaction), as txnList is ListDocuments in
// one: a read-only transaction reads its snapshot; a pessimistic one takes a shared lock on the
// scope first and holds it; an optimistic one records the page, which its commit reads again.
func (s *PGStore) txnScan(ctx context.Context, db DB, r ScanRequest) (ListResult, error) {
	ref := *r.Transaction
	r.Transaction = nil
	if !r.ReadTime.IsZero() {
		return ListResult{}, errTxnReadTime
	}
	tx, err := s.txns.use(db, ref)
	if err != nil {
		return ListResult{}, err
	}
	switch tx.mode {
	case txnReadOnly:
		r.ReadTime = tx.readTime
		return s.Scan(ctx, db, r)
	case txnPessimistic:
		if err := tx.table.acquire(ctx, tx.lt, r.lock(), lockShared); err != nil {
			return ListResult{}, s.txns.failed(tx, err)
		}
	}
	res, err := s.Scan(ctx, db, r)
	if err != nil {
		return ListResult{}, err
	}
	var list *listRead
	if tx.mode == txnOptimistic {
		list = &listRead{scan: &r, lock: r.lock(), docs: versions(res.Documents)}
	}
	if err := s.txns.record(tx, res.Documents, list); err != nil {
		return ListResult{}, err
	}
	return res, nil
}
