package core

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// Options configures a PGStore.
type Options struct {
	// LocationID is what a new database reports as its location; "" is DefaultLocation.
	LocationID string
	// CommitConcurrency is the commit gate of each database; 0 is DefaultCommitConcurrency.
	CommitConcurrency int
	// Notifier hears of every commit; nil for none.
	Notifier Notifier
	// Instance, when set, makes every call fail with ErrNotServing while this process does not
	// hold the single-instance lock (ADR-024 §7). Tests and tools leave it nil.
	Instance *Instance
	// Transactions bounds the transactions and keys their ids.
	Transactions TxnConfig
	// IndexLimits bounds the Admin API's index configuration; zero values are the defaults.
	IndexLimits IndexLimits
	// ChangelogRetention is how long changelog rows are kept: a retention pass prunes none
	// younger, and an index read continued in a later snapshot relies on it (IndexRead). Default
	// 1 hour; less than MinChangelogRetention is raised to it.
	ChangelogRetention time.Duration
}

// PGStore is Store on Postgres, schema documents (ADR-024).
type PGStore struct {
	pool     *pgxpool.Pool
	q        *queries.Queries
	seq      *sequencer
	location string
	instance *Instance
	txns     *transactions
	cats     *catalogs
	// builds wakes the index build job (RunIndexBuilds).
	builds chan struct{}
	// limits bounds a database's composite indexes and field overrides.
	limits IndexLimits
	// changelogRetention is Options.ChangelogRetention.
	changelogRetention time.Duration
	// ttl counts the TTL sweeper's work (ttl.go).
	ttl ttlStats
}

var _ Store = (*PGStore)(nil)

// NewPGStore returns the Postgres store.
func NewPGStore(pool *pgxpool.Pool, o Options) *PGStore {
	cats := newCatalogs(pool)
	return &PGStore{
		pool:     pool,
		q:        queries.New(pool),
		seq:      newSequencer(pool, cmp.Or(o.CommitConcurrency, DefaultCommitConcurrency), o.Notifier, cats),
		location: cmp.Or(o.LocationID, DefaultLocation),
		instance: o.Instance,
		txns:     newTransactions(o.Transactions),
		cats:     cats,
		builds:   make(chan struct{}, 1),
		limits:   o.IndexLimits.withDefaults(),

		changelogRetention: changelogRetention(o.ChangelogRetention),
	}
}

// LargeCommits is how many commits were accepted whose index entry changes, summed over their
// documents, pass the bound production holds one document's to: production's answer to such a
// commit is not recorded (indexentries.go).
func (s *PGStore) LargeCommits() int64 { return s.seq.largeCommits.Load() }

func (s *PGStore) serving() error {
	if s.instance != nil && !s.instance.Held() {
		return ErrNotServing
	}
	return nil
}

// Commit implements Store.
func (s *PGStore) Commit(ctx context.Context, db DB, writes []Write) (CommitResult, error) {
	return s.commit(ctx, db, writes, nil)
}

// commit runs writes as one commit, in a transaction when c is set, under the lock manager's
// locks (lockCommit).
func (s *PGStore) commit(ctx context.Context, db DB, writes []Write, c *commitTxn) (CommitResult, error) {
	if err := s.serving(); err != nil {
		return CommitResult{}, err
	}
	p := newPlan(writes)
	release, err := s.lockCommit(ctx, db, p, c)
	if err != nil {
		return CommitResult{}, err
	}
	defer release()
	res, err := s.seq.commit(ctx, db, p)
	if errors.Is(err, errNoDatabase) {
		if err := s.missing(ctx, db); err != nil {
			return CommitResult{}, err
		}
		res, err = s.seq.commit(ctx, db, p)
	}
	if errors.Is(err, errNoDatabase) {
		return CommitResult{}, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	return res, err
}

// head is what a read statement returns of the sequencer beside its rows.
type head struct {
	seq      int64
	lastTS   time.Time
	earliest time.Time
	// readTime is the time the read answers at: lastTS, or the read time asked.
	readTime time.Time
}

// docRow is a document row a read returned: name is its whole name key.
type docRow struct {
	name       []byte
	doc        []byte
	createTime time.Time
	updateTime time.Time
}

// read runs a read statement, built by build for read time t (zero: the latest), and answers
// with its head and rows. A missing '(default)' is created and the read runs again. A read at t
// checks t against the database's window, and when t is past the last commit it first waits for
// the commit that may be between its stamp and COMMIT (ADR-024 §3).
func (s *PGStore) read(ctx context.Context, db DB, t time.Time, build func(t time.Time) (string, []any)) (head, []docRow, error) {
	if err := s.serving(); err != nil {
		return head{}, nil, err
	}
	ensured, waited := false, false
	for {
		sql, args := build(t)
		h, rows, found, err := s.run(ctx, sql, args, !t.IsZero())
		switch {
		case err != nil:
			return head{}, nil, err
		case !found && ensured:
			return head{}, nil, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
		case !found:
			if err := s.missing(ctx, db); err != nil {
				return head{}, nil, err
			}
			ensured = true
			continue
		case t.IsZero():
			h.readTime = h.lastTS
			return h, rows, nil
		case t.Before(h.earliest):
			return head{}, nil, ErrReadTimeTooOld
		case !t.After(h.lastTS) || waited:
			h.readTime = t
			return h, rows, nil
		}
		now, err := s.q.WaitHead(ctx, queries.WaitHeadParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		if err != nil {
			return head{}, nil, err
		}
		if t.After(now) {
			return head{}, nil, ErrReadTimeFuture
		}
		waited = true
	}
}

// run runs a read statement whose rows are (seq, last_ts[, earliest], name, doc, create_time,
// update_time), the document columns NULL on the one row of a read that found none. found is
// false when the database has no sequencer row.
func (s *PGStore) run(ctx context.Context, sql string, args []any, earliest bool) (head, []docRow, bool, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return head{}, nil, false, err
	}
	defer rows.Close()
	var h head
	var out []docRow
	found := false
	for rows.Next() {
		var r docRow
		var ct, ut *time.Time
		dest := []any{&h.seq, &h.lastTS, &r.name, &r.doc, &ct, &ut}
		if earliest {
			dest = []any{&h.seq, &h.lastTS, &h.earliest, &r.name, &r.doc, &ct, &ut}
		}
		if err := rows.Scan(dest...); err != nil {
			return head{}, nil, false, err
		}
		found = true
		if r.name != nil {
			r.createTime, r.updateTime = *ct, *ut
			out = append(out, r)
		}
	}
	return h, out, found, rows.Err()
}

// rangeAt returns every document at t with a stored name key in [lo, hi].
func (s *PGStore) rangeAt(ctx context.Context, db DB, lo, hi []byte, t time.Time) ([]docRow, error) {
	rows, err := s.q.RangeAt(ctx, queries.RangeAtParams{ProjectNumber: db.Project, DatabaseID: db.ID, Lo: lo, Hi: hi, ReadTime: t})
	if err != nil {
		return nil, err
	}
	out := make([]docRow, len(rows))
	for i, r := range rows {
		out[i] = docRow{name: r.Name, doc: r.Doc, createTime: r.CreateTime, updateTime: r.UpdateTime}
	}
	return out, nil
}

func (r docRow) document() (Document, error) {
	p, err := pathOfKey(r.name)
	if err != nil {
		return Document{}, err
	}
	fields, _, _, err := decodeDocument(r.doc)
	if err != nil {
		return Document{}, err
	}
	return Document{Path: p, Fields: fields, CreateTime: r.createTime, UpdateTime: r.updateTime}, nil
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, db DB, p Path, o ReadOptions) (Document, time.Time, error) {
	docs, readTime, err := s.BatchGet(ctx, db, []Path{p}, o)
	if err != nil {
		return Document{}, time.Time{}, err
	}
	if !docs[0].Exists() {
		return Document{}, time.Time{}, documentNotFound(db.DocumentName(p))
	}
	return docs[0], readTime, nil
}

// BatchGet implements Store.
func (s *PGStore) BatchGet(ctx context.Context, db DB, paths []Path, o ReadOptions) ([]Document, time.Time, error) {
	if o.Transaction != nil {
		return s.txnBatchGet(ctx, db, paths, o)
	}
	keys := make([][]byte, len(paths))
	for i, p := range paths {
		keys[i] = storedKey(nameKey(p))
	}
	h, rows, err := s.read(ctx, db, o.ReadTime, func(t time.Time) (string, []any) {
		return statement(func(q *queries.Queries) error {
			if t.IsZero() {
				_, err := q.GetDocuments(ctx, queries.GetDocumentsParams{ProjectNumber: db.Project, DatabaseID: db.ID, NameKeys: keys})
				return err
			}
			_, err := q.GetDocumentsAt(ctx, queries.GetDocumentsAtParams{ProjectNumber: db.Project, DatabaseID: db.ID, NameKeys: keys, ReadTime: t})
			return err
		})
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	found := make(map[string]Document, len(rows))
	for _, r := range rows {
		d, err := r.document()
		if err != nil {
			return nil, time.Time{}, err
		}
		found[string(r.name)] = d
	}
	out := make([]Document, len(paths))
	for i, p := range paths {
		d, ok := found[string(nameKey(p))]
		if !ok {
			d = Document{Path: p}
		}
		out[i] = d
	}
	return out, h.readTime, nil
}

func pageSize(n int) int {
	if n <= 0 || n > MaxPageSize {
		return MaxPageSize
	}
	return n
}

// ListDocuments implements Store.
func (s *PGStore) ListDocuments(ctx context.Context, db DB, r ListRequest) (ListResult, error) {
	if r.Transaction != nil {
		return s.txnList(ctx, db, r)
	}
	if r.After != nil && !r.after() {
		return ListResult{}, ErrInvalidAfter
	}
	n := pageSize(r.PageSize)
	var docs []Document
	var readTime time.Time
	var err error
	switch {
	case r.CollectionID == "":
		docs, readTime, err = s.listAll(ctx, db, r, n+1)
	case r.ShowMissing:
		docs, readTime, err = s.listWithMissing(ctx, db, r, n+1)
	default:
		var after []byte
		if r.After != nil {
			after = nameKey(r.After)
		}
		docs, readTime, err = s.collectionPage(ctx, db, r.Parent, r.CollectionID, after, n+1, r.ReadTime)
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

// collectionPage returns up to limit documents of the collection id under parent whose name key
// is above after (nil: from the first), in name order, at t (zero: the latest), and the read time.
func (s *PGStore) collectionPage(ctx context.Context, db DB, parent Path, id string, after []byte, limit int, t time.Time) ([]Document, time.Time, error) {
	cp := collectionPrefix(parent, id)
	hash := sha256.Sum256(cp)
	parentKey := []byte{}
	if len(parent) > 0 {
		parentKey = nameKey(parent)
	}
	if after == nil {
		after = []byte{}
	}
	h, rows, err := s.read(ctx, db, t, func(t time.Time) (string, []any) {
		return statement(func(q *queries.Queries) error {
			if t.IsZero() {
				_, err := q.ListCollection(ctx, queries.ListCollectionParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					ParentHash: hash[:], Parent: parentKey, CollectionID: id, Lo: lowerBound(after), After: after, RowLimit: int32(limit)})
				return err
			}
			_, err := q.ListCollectionAt(ctx, queries.ListCollectionAtParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				ParentHash: hash[:], Parent: parentKey, CollectionID: id, Lo: lowerBound(after), After: after, RowLimit: int32(limit), ReadTime: t})
			return err
		})
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	// The rows come in stored-key order, which is name order but within a run of keys cut
	// alike. Every run is whole but perhaps the last: when the page was full, read the rest of
	// it, then order all by name.
	if len(rows) == limit && len(rows[limit-1].name) > cutLen {
		lo, hi := chainBounds(rows[limit-1].name)
		run, err := s.rangeAt(ctx, db, lo, hi, h.readTime)
		if err != nil {
			return nil, time.Time{}, err
		}
		seen := map[string]bool{}
		for _, r := range rows {
			seen[string(r.name)] = true
		}
		for _, r := range run {
			if !seen[string(r.name)] && bytes.Compare(r.name, after) > 0 && inCollection(r.name, cp) {
				rows = append(rows, r)
			}
		}
	}
	slices.SortFunc(rows, func(a, b docRow) int { return bytes.Compare(a.name, b.name) })
	rows = rows[:min(len(rows), limit)]
	docs := make([]Document, len(rows))
	for i, r := range rows {
		if docs[i], err = r.document(); err != nil {
			return nil, time.Time{}, err
		}
	}
	return docs, h.readTime, nil
}

// inCollection reports whether a whole name key is a document directly in the collection whose
// prefix key is cp.
func inCollection(name, cp []byte) bool {
	if !bytes.HasPrefix(name, cp) {
		return false
	}
	end, err := childEnd(name, len(cp))
	return err == nil && end == len(name)-1
}

// child is one child of a key prefix: a document of a collection, or a collection of a document.
type child struct {
	prefix []byte  // the child's own prefix key
	row    *docRow // the child document's row, when the child is a document that exists
}

// children returns up to limit children of a key prefix, in order, from the first past the
// subtree of the child whose prefix key is after (nil: from the first), at t (zero: the latest),
// and the read time. It skips each child's subtree in one index probe (WalkChildren). Children
// whose keys are cut alike (ADR-024 §1.4) are one run: read whole and ordered here.
func (s *PGStore) children(ctx context.Context, db DB, prefix, after []byte, limit int, t time.Time) ([]child, time.Time, error) {
	plen := len(prefix)
	from := append(bytes.Clone(prefix), segNumeric)
	if after != nil {
		from = append(bytes.Clone(after), pastChildren)
	}
	if plen >= cutLen {
		// Every key below the prefix is cut inside it: the children are one run.
		h, _, err := s.read(ctx, db, t, func(t time.Time) (string, []any) { return s.headStatement(ctx, db, t) })
		if err != nil {
			return nil, time.Time{}, err
		}
		lo, hi := chainBounds(prefix)
		out, err := s.appendRun(ctx, db, nil, lo, hi, prefix, from, h.readTime)
		if err != nil {
			return nil, time.Time{}, err
		}
		return out[:min(len(out), limit)], h.readTime, nil
	}
	hi := append(bytes.Clone(prefix), pastChildren) // at most cutLen bytes: exact
	h, rows, err := s.read(ctx, db, t, func(t time.Time) (string, []any) {
		return statement(func(q *queries.Queries) error {
			if t.IsZero() {
				_, err := q.WalkChildren(ctx, queries.WalkChildrenParams{ProjectNumber: db.Project, DatabaseID: db.ID,
					Lo: lowerBound(from), Hi: hi, After: from, Plen: int32(plen), MaxSteps: int32(limit)})
				return err
			}
			_, err := q.WalkChildrenAt(ctx, queries.WalkChildrenAtParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				Lo: lowerBound(from), Hi: hi, After: from, Plen: int32(plen), MaxSteps: int32(limit), ReadTime: t})
			return err
		})
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	var out []child
	for _, r := range rows {
		end, err := childEnd(r.name, plen)
		if err != nil {
			return nil, time.Time{}, err
		}
		if end < cutLen {
			c := child{prefix: r.name[:end]}
			if len(r.name) == end+1 {
				c.row = &r
			}
			out = append(out, c)
			continue
		}
		// The child's prefix reaches the cut: its keys, and its cut-alike siblings', are one run,
		// which the walk stepped over at once.
		lo, hi := chainBounds(r.name)
		if out, err = s.appendRun(ctx, db, out, lo, hi, prefix, from, h.readTime); err != nil {
			return nil, time.Time{}, err
		}
	}
	return out[:min(len(out), limit)], h.readTime, nil
}

// appendRun appends the children of prefix, from from on, that the documents at t with a stored
// key in [lo, hi] are or are under, in order.
func (s *PGStore) appendRun(ctx context.Context, db DB, out []child, lo, hi, prefix, from []byte, t time.Time) ([]child, error) {
	run, err := s.rangeAt(ctx, db, lo, hi, t)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(run, func(a, b docRow) int { return bytes.Compare(a.name, b.name) })
	for _, x := range run {
		if !bytes.HasPrefix(x.name, prefix) || bytes.Compare(x.name, from) < 0 {
			continue
		}
		end, err := childEnd(x.name, len(prefix))
		if err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || !bytes.Equal(out[n-1].prefix, x.name[:end]) {
			out = append(out, child{prefix: x.name[:end]})
		}
		if len(x.name) == end+1 {
			out[len(out)-1].row = &x
		}
	}
	return out, nil
}

// headStatement is a read of the sequencer alone: GetDocuments of no document.
func (s *PGStore) headStatement(ctx context.Context, db DB, t time.Time) (string, []any) {
	return statement(func(q *queries.Queries) error {
		if t.IsZero() {
			_, err := q.GetDocuments(ctx, queries.GetDocumentsParams{ProjectNumber: db.Project, DatabaseID: db.ID})
			return err
		}
		_, err := q.GetDocumentsAt(ctx, queries.GetDocumentsAtParams{ProjectNumber: db.Project, DatabaseID: db.ID, ReadTime: t})
		return err
	})
}

// listWithMissing lists a collection's children, existing documents and missing ones.
func (s *PGStore) listWithMissing(ctx context.Context, db DB, r ListRequest, limit int) ([]Document, time.Time, error) {
	var after []byte
	if r.After != nil {
		after = prefixKey(r.After)
	}
	kids, readTime, err := s.children(ctx, db, collectionPrefix(r.Parent, r.CollectionID), after, limit, r.ReadTime)
	if err != nil {
		return nil, time.Time{}, err
	}
	docs := make([]Document, len(kids))
	for i, c := range kids {
		if c.row != nil {
			docs[i], err = c.row.document()
		} else {
			docs[i].Path, err = pathOfKey(append(bytes.Clone(c.prefix), keyEnd))
		}
		if err != nil {
			return nil, time.Time{}, err
		}
	}
	return docs, readTime, nil
}

// after reports whether After is a document of the listing: in a collection directly under
// Parent, and in CollectionID when one is named.
func (r ListRequest) after() bool {
	return r.After.IsDocument() && slices.Equal(r.After.Parent(), r.Parent) &&
		(r.CollectionID == "" || r.After.CollectionID() == r.CollectionID)
}

// listAll lists the documents of every collection directly under r.Parent, collection by
// collection in id order, which is name order.
func (s *PGStore) listAll(ctx context.Context, db DB, r ListRequest, limit int) ([]Document, time.Time, error) {
	t := r.ReadTime
	var docs []Document
	var readTime time.Time
	take := func(id string, after []byte) error {
		page, rt, err := s.collectionPage(ctx, db, r.Parent, id, after, limit-len(docs), t)
		if err != nil {
			return err
		}
		t, readTime = rt, rt // later reads see the snapshot of the first
		docs = append(docs, page...)
		return nil
	}
	var last []byte // the prefix key of the last collection listed
	if r.After != nil {
		if err := take(r.After.CollectionID(), nameKey(r.After)); err != nil {
			return nil, time.Time{}, err
		}
		last = collectionPrefix(r.Parent, r.After.CollectionID())
	}
	parent := prefixKey(r.Parent)
	const batch = 64
	for len(docs) < limit {
		kids, rt, err := s.children(ctx, db, parent, last, batch, t)
		if err != nil {
			return nil, time.Time{}, err
		}
		t, readTime = rt, rt
		for _, c := range kids {
			id, _, err := segmentAt(c.prefix, len(parent))
			if err != nil {
				return nil, time.Time{}, err
			}
			if err := take(id, nil); err != nil {
				return nil, time.Time{}, err
			}
			last = c.prefix
			if len(docs) >= limit {
				break
			}
		}
		if len(kids) < batch {
			break
		}
	}
	return docs, readTime, nil
}

// ListCollectionIDs implements Store.
func (s *PGStore) ListCollectionIDs(ctx context.Context, db DB, r ListCollectionIDsRequest) (CollectionIDs, error) {
	if r.Transaction != nil {
		return CollectionIDs{}, errTxnListCollectionIDs
	}
	n := pageSize(r.PageSize)
	parent := prefixKey(r.Parent)
	var after []byte
	if r.After != "" {
		after = collectionPrefix(r.Parent, r.After)
	}
	kids, readTime, err := s.children(ctx, db, parent, after, n+1, r.ReadTime)
	if err != nil {
		return CollectionIDs{}, err
	}
	res := CollectionIDs{IDs: make([]string, 0, len(kids)), ReadTime: readTime}
	for _, c := range kids[:min(len(kids), n)] {
		id, _, err := segmentAt(c.prefix, len(parent))
		if err != nil {
			return CollectionIDs{}, err
		}
		res.IDs = append(res.IDs, id)
	}
	res.More = len(kids) > n
	return res, nil
}
