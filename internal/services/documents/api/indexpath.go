package api

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The index paths (ADR-025 §1): a query the planner serves exactly (query.Plan) reads index
// entries in place of the reference path's candidates, every candidate checked again by the
// reference matcher (query.Recheck) and then run by the reference runner in the order the
// entries come. A read at a read_time or in a transaction stays on the reference path: entries
// hold the latest state alone, and a transaction's reads take its locks and its record of reads
// on the reference path's scope.
//
// A query answers at one read time, the commit of its first snapshot (core.IndexRead), and never
// holds a snapshot, nor its pool connection, while it sends to its client: it reads a bounded
// batch of results, releases the snapshot, sends the batch, and continues in a new snapshot
// from where its scans stopped. There an entry stands for the first snapshot's when its document
// has not changed since the read time (a candidate whose update_time is later is dropped), and
// the documents that changed come from the changelog as they were at the read time
// (core.IndexRead.ChangedSince), merged into the results in the query's order: the answer is the
// first snapshot's, as the reference path's at that read time.
//
// Memory is bounded per query: a scan buffers a page of entries (keys and names); documents are
// fetched a few at a time within a byte budget a union's streams share; a batch holds at most
// batchBytes; and the documents a continued read keeps of those that changed since its read
// time are bounded as the reference path bounds what it keeps (query.DefaultMaxKeptBytes).

const (
	// firstPage and maxPage bound the entries a scan reads at a time, and the results of a
	// batch: the first page is what the offset and the limit want, and each page after it
	// doubles, up to maxPage (divided among a union's streams for the entries).
	firstPage = 100
	maxPage   = 1000
	// batchBytes bounds the documents of one batch.
	batchBytes = 4 << 20
	// fetchBudget bounds the documents a query's streams fetch at a time, shared by a union's
	// streams; minFetch is a stream's least. A fetch returns one document at least.
	fetchBudget = 8 << 20
	minFetch    = 256 << 10
	// maxChangedBytes bounds the documents a continued read keeps of those that changed since
	// its read time.
	maxChangedBytes = query.DefaultMaxKeptBytes
)

var (
	// errChangedBound is a continued read that would keep more than maxChangedBytes of the
	// documents that changed since its read time, as the reference path's bound fails a query.
	errChangedBound = wire.NewError(wire.CodeResourceExhausted,
		"A query this server serves from an index keeps at most 256 MiB of the documents that change while it runs; "+
			"read its results faster or narrow it.")
	// errIndexChanged is a continued read whose indexes changed since its read time.
	errIndexChanged = wire.NewError(wire.CodeAborted, "An index the query reads changed while it ran. Run it again.")
)

// runIndexed serves q from an index when the planner serves it: ok is false when it does not,
// before anything was sent, and the reference path serves q.
func (s *Service) runIndexed(ctx context.Context, a admitted, parent wire.Name, q *query.Query, ro core.ReadOptions,
	emit func(d *query.Doc, readTime time.Time, skipped int) error) (skipped int, readTime time.Time, ok bool, err error) {
	if s.o.ReferenceOnly || ro.Transaction != nil || !ro.ReadTime.IsZero() || q.AllDescendants && !parent.IsRoot() {
		return 0, time.Time{}, false, nil
	}
	// The cached catalogue first, so a query no index serves opens no snapshot.
	cat, err := s.o.Store.IndexCatalog(ctx, a.db)
	if err != nil {
		return 0, time.Time{}, false, s.fail(ctx, err)
	}
	if _, ok := q.Plan(cat); !ok {
		if q.OnFields() {
			// A shape the enforcement let through and the planner refused: a performance
			// opportunity, counted and logged (ADR-025 §1).
			query.Unplanned()
			s.log.Debug("documents: a query on fields is served on the reference path", "project", a.wdb.Project,
				"collection", q.CollectionID)
		}
		return 0, time.Time{}, false, nil
	}
	rd, err := s.o.Store.BeginIndexRead(ctx, a.db)
	if err != nil {
		return 0, time.Time{}, false, s.fail(ctx, err)
	}
	x := &executor{s: s, rd: rd, a: a, parent: parent, q: q, page: firstPage, readTime: rd.ReadTime(), seq: rd.Seq(),
		catVersion: rd.CatalogVersion(), fetchKeys: firstPage, changed: map[string]bool{},
		scope: core.ChangeScope{Parent: pathOf(parent), CollectionID: q.CollectionID, Group: q.AllDescendants}}
	defer x.release(ctx)
	plan, ok := q.Plan(rd.Catalog())
	if !ok {
		return 0, time.Time{}, false, nil
	}
	if q.Limit > 0 {
		x.page = min(max(q.Offset+q.Limit, 1), maxPage)
	}
	u, ok := x.build(plan)
	if !ok {
		return 0, time.Time{}, false, nil
	}
	if b := s.indexBatch; b > 0 {
		// Tests: batches of b results, pages of b+1 entries and fetches of one document: scans
		// and fetches continue in later snapshots.
		x.page, x.maxPage, x.maxBatch, x.budget = b+1, b+1, b, 1
	}
	var runner *query.Runner
	runner = query.NewOrderedRunner(q, func(d *query.Doc) error { return emit(d, x.readTime, runner.Skipped()) })
	err = x.run(ctx, u, runner)
	if err == nil {
		err = runner.Finish()
	}
	if s.observePeak != nil {
		s.observePeak(x.peak)
	}
	if errors.Is(err, errAggregationDone) {
		err = nil
	}
	if re, isMismatch := errors.AsType[*query.RecheckError](err); isMismatch {
		s.log.Error("documents: index path recheck mismatch", "project", a.wdb.Project, "collection", q.CollectionID,
			"document", re.Name, "reason", re.Reason)
		return 0, time.Time{}, true, wire.Internal()
	}
	if err != nil {
		return 0, time.Time{}, true, s.fail(ctx, err)
	}
	query.Served(plan.Path)
	return runner.Skipped(), x.readTime, true, nil
}

// executor builds and reads a plan's streams, batch by batch, each batch in a snapshot of its
// own at the first snapshot's commit (gen counts them).
type executor struct {
	s      *Service
	rd     *core.IndexRead // nil between batches
	a      admitted
	parent wire.Name
	q      *query.Query
	page   int
	// maxPage bounds a scan's page: maxPage divided among a union's streams; maxBatch the
	// results of a batch.
	maxPage, maxBatch int

	plan       *query.Plan
	readers    []*scanReader
	readTime   time.Time
	catVersion int64
	// seq is the commit of the latest snapshot: ChangedSince reads the commits after it.
	seq int64
	gen int
	// scope is what the query reads; changed the names of its documents that changed since the
	// read time, whose images at the read time the union holds (or held) already.
	scope   core.ChangeScope
	changed map[string]bool

	// fetchKeys is how many names a fetch asks for, budget the bytes it may return.
	fetchKeys int
	budget    int
	// held is the bytes of the documents the streams and the current batch hold, peak its
	// most; changedBytes is what the union holds of the documents that changed.
	held, peak   int
	changedBytes int
}

// stream is candidates in the query's order; next returns nil past the last.
type stream interface {
	next(ctx context.Context) (*query.Doc, error)
}

// build returns the union of the plan's streams and of the documents that change while the
// query runs. ok is false when an index the plan names is not in the read's catalogue.
func (x *executor) build(p *query.Plan) (*unionStream, bool) {
	x.plan = p
	x.maxPage, x.maxBatch = max(maxPage/len(p.Streams), 16), maxPage
	x.budget = max(fetchBudget/len(p.Streams), minFetch)
	x.page = min(x.page, x.maxPage)
	u := &unionStream{x: x, changed: &changedStream{}}
	for i := range p.Streams {
		st := &p.Streams[i]
		scans := make([]*scanReader, 0, len(st.Scans))
		for j := range st.Scans {
			r, ok := x.scan(&st.Scans[j])
			if !ok {
				return nil, false
			}
			scans = append(scans, r)
		}
		x.readers = append(x.readers, scans...)
		check := x.q.Recheck(st)
		if len(scans) == 1 {
			u.streams = append(u.streams, &scanStream{x: x, r: scans[0], check: check})
		} else {
			u.streams = append(u.streams, &mergedStream{x: x, scans: scans, check: check})
		}
	}
	u.streams = append(u.streams, u.changed)
	u.heads, u.started = make([]*query.Doc, len(u.streams)), make([]bool, len(u.streams))
	return u, true
}

// run reads the union batch by batch, each in its snapshot, and adds each batch to the runner
// once the snapshot is released.
func (x *executor) run(ctx context.Context, u *unionStream, runner *query.Runner) error {
	n := min(x.page, x.maxBatch)
	for {
		batch, more, err := x.batch(ctx, u, n)
		if err != nil {
			return err
		}
		x.release(ctx)
		for i, d := range batch {
			done, err := runner.Add(d)
			batch[i] = nil
			x.hold(-docBytes(d))
			if err != nil || done {
				return err
			}
		}
		if !more {
			return nil
		}
		if x.s.afterBatch != nil {
			x.s.afterBatch()
		}
		n = min(2*n, x.maxBatch)
		if err := x.resume(ctx, u); err != nil {
			return err
		}
	}
}

// batch reads up to n results of the union, within batchBytes: more is false past the last.
func (x *executor) batch(ctx context.Context, u *unionStream, n int) (out []*query.Doc, more bool, err error) {
	size := 0
	for len(out) < n && size < batchBytes {
		d, err := u.next(ctx)
		if err != nil || d == nil {
			return out, false, err
		}
		out = append(out, d)
		size += docBytes(d)
	}
	return out, true, nil
}

// release ends the current snapshot.
func (x *executor) release(ctx context.Context) {
	if x.rd != nil {
		x.rd.Close(ctx)
		x.rd = nil
	}
}

// resume begins the next batch's snapshot: it checks that the plan still reads the same
// indexes, and adds to the union the documents that changed since the snapshot before, as they
// were at the read time.
func (x *executor) resume(ctx context.Context, u *unionStream) error {
	rd, err := x.s.o.Store.BeginIndexRead(ctx, x.a.db)
	if err != nil {
		return err
	}
	x.rd = rd
	x.gen++
	if rd.CatalogVersion() != x.catVersion {
		p, ok := x.q.Plan(rd.Catalog())
		if !ok || !reflect.DeepEqual(p, x.plan) {
			return errIndexChanged
		}
		for _, r := range x.readers {
			if id, ok := rd.IndexID(r.sc.Index, r.sc.Single); !ok || id != r.id {
				return errIndexChanged
			}
		}
		x.catVersion = rd.CatalogVersion()
	}
	docs, err := rd.ChangedSince(ctx, x.seq, x.scope)
	if err != nil {
		return err
	}
	x.seq = rd.Seq()
	var add []*query.Doc
	for _, d := range docs {
		name := x.a.db.DocumentName(d.Path)
		if x.changed[name] {
			continue // changed before: its image at the read time is the first change's
		}
		x.changed[name] = true
		if !d.Exists() {
			continue // created since the read time
		}
		qd := &query.Doc{Name: name, Fields: d.Fields, CreateTime: d.CreateTime, UpdateTime: d.UpdateTime}
		if x.q.Where != nil && !x.q.Where.Matches(qd) || !hasOrderFields(x.q, qd) {
			continue
		}
		add = append(add, qd)
	}
	return u.addChanged(add)
}

// hasOrderFields reports whether d has every field q orders by, as each of an index path's
// candidates has.
func hasOrderFields(q *query.Query, d *query.Doc) bool {
	for _, o := range q.Order {
		if _, ok := d.Fields.Lookup(o.Path); !ok && !o.Path.IsDocumentID() {
			return false
		}
	}
	return true
}

// hold counts n bytes of documents held, or released when negative.
func (x *executor) hold(n int) {
	x.held += n
	x.peak = max(x.peak, x.held)
}

func docBytes(d *query.Doc) int { return value.DocumentSize(d.Name, d.Fields) }

// scan returns the reader of one scan: its index's id and key range.
func (x *executor) scan(sc *query.Scan) (*scanReader, bool) {
	id, ok := x.rd.IndexID(sc.Index, sc.Single)
	if !ok {
		return nil, false
	}
	base := core.IndexKeyPrefix(sc.Index.Scope, core.Path(x.parent.Path), x.q.CollectionID)
	for _, p := range sc.Prefix {
		base, _ = x.appendPart(base, p)
	}
	r := &scanReader{x: x, sc: sc, id: id, base: base, lo: base, hi: successor(base)}
	for _, b := range sc.Lo {
		if k, ok := x.bound(base, b); ok && (k == nil || bytes.Compare(k, r.lo) > 0) {
			r.lo = k
		}
	}
	for _, b := range sc.Hi {
		if k, ok := x.bound(base, b); ok && k != nil && (r.hi == nil || bytes.Compare(k, r.hi) < 0) {
			r.hi = k
		}
	}
	// A lower bound past every key (nil) or at or past the upper bound leaves nothing.
	r.done = r.lo == nil || r.hi != nil && bytes.Compare(r.lo, r.hi) >= 0
	return r, true
}

// bound returns a bound's key past base: nil past every key. ok is false for a bound no key
// can hold (a name of another database), which the scan then does without: the runner checks
// what it bounds.
func (x *executor) bound(base []byte, b query.Bound) ([]byte, bool) {
	k := bytes.Clone(base)
	for _, p := range b.Parts {
		var ok bool
		if k, ok = x.appendPart(k, p); !ok {
			return nil, false
		}
	}
	if b.After {
		return successor(k), true
	}
	return k, true
}

// appendPart appends a part of a key: a value's, a name's or a class edge's.
func (x *executor) appendPart(dst []byte, p query.Part) ([]byte, bool) {
	switch {
	case p.Edge != nil:
		start := len(dst)
		dst = append(dst, p.Edge...)
		if p.Desc {
			for i := start; i < len(dst); i++ {
				dst[i] = ^dst[i]
			}
		}
		return dst, true
	case p.Name:
		n, err := wire.ParseReference(p.Value.StringValue())
		if err != nil || n.Database != x.a.wdb {
			return dst, false
		}
		// The name's part without the length that ends an entry's key.
		dst = core.AppendIndexName(dst, pathOf(n), p.Desc)
		return dst[:len(dst)-2], true
	}
	return core.AppendIndexValue(dst, p.Value, p.Desc), true
}

// successor returns the first key past every key that starts with k: nil when there is none.
func successor(k []byte) []byte {
	out := bytes.Clone(k)
	for len(out) > 0 && out[len(out)-1] == 0xFF {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil
	}
	out[len(out)-1]++
	return out
}

// scanReader reads one scan's entries page by page, each page in the snapshot of the batch that
// reads it (gen).
type scanReader struct {
	x      *executor
	sc     *query.Scan
	id     int64
	base   []byte
	lo, hi []byte
	page   int
	buf    []core.IndexHit
	gen    int
	done   bool
}

// head returns the reader's next entry without taking it; nil past the last.
func (r *scanReader) head(ctx context.Context) (*core.IndexHit, error) {
	if len(r.buf) == 0 && !r.done {
		if r.page == 0 {
			r.page = r.x.page
		}
		hits, read, err := r.x.rd.Scan(ctx, core.IndexRange{Index: r.id, Lo: r.lo, Hi: r.hi, Limit: r.page})
		query.EntriesRead(read)
		if err != nil {
			return nil, err
		}
		if len(hits) < r.page {
			r.done = true
		} else {
			// Past the last key read: no key is a prefix of another.
			r.lo = append(bytes.Clone(hits[len(hits)-1].Key), 0x00)
		}
		r.page = min(2*r.page, r.x.maxPage)
		r.buf, r.gen = hits, r.x.gen
	}
	if len(r.buf) == 0 {
		return nil, nil
	}
	return &r.buf[0], nil
}

// take takes the head.
func (r *scanReader) take() { r.buf = r.buf[1:] }

// suffix is a key past the scan's prefix: for the equality scans a merge intersects, the
// document's name in the scan's direction.
func (r *scanReader) suffix(h *core.IndexHit) []byte { return h.Key[len(r.base):] }

// seek moves the reader to its first entry whose suffix is at or past target.
func (r *scanReader) seek(target []byte) {
	for len(r.buf) > 0 && bytes.Compare(r.suffix(&r.buf[0]), target) < 0 {
		r.buf = r.buf[1:]
	}
	if len(r.buf) == 0 && !r.done {
		if k := append(bytes.Clone(r.base), target...); bytes.Compare(k, r.lo) > 0 {
			r.lo = k
		}
		if r.hi != nil && bytes.Compare(r.lo, r.hi) >= 0 {
			r.done = true
		}
	}
}

// candidate is a document an index entry names, with the batch whose snapshot read the entry.
type candidate struct {
	path core.Path
	gen  int
}

// fetch reads the documents of the first of cands within the budget, in the current snapshot,
// and returns those check keeps, and how many of cands it read. A candidate whose document
// changed since the read time is dropped (the union has its image at the read time), and so is
// one whose document is gone when an earlier snapshot read its entry; in the snapshot that read
// the entry, a document that is gone is a mismatch, which check reports.
func (x *executor) fetch(ctx context.Context, cands []candidate, check *query.Recheck) ([]*query.Doc, int, error) {
	n := min(len(cands), x.fetchKeys)
	paths := make([]core.Path, n)
	for i := range paths {
		paths[i] = cands[i].path
	}
	docs, err := x.rd.DocumentsWithin(ctx, paths, x.budget)
	if err != nil {
		return nil, 0, err
	}
	// Ask for more names while the budget holds every document asked for, fewer otherwise.
	if len(docs) == n {
		x.fetchKeys = min(2*x.fetchKeys, x.maxPage)
	} else {
		x.fetchKeys = max(2*len(docs), 1)
	}
	out := make([]*query.Doc, 0, len(docs))
	for i, d := range docs {
		if d.Exists() && d.UpdateTime.After(x.readTime) || !d.Exists() && cands[i].gen < x.gen {
			continue
		}
		qd := &query.Doc{Name: x.a.db.DocumentName(d.Path), Fields: d.Fields, CreateTime: d.CreateTime, UpdateTime: d.UpdateTime}
		keep, err := check.Check(qd)
		if err != nil {
			return nil, 0, err
		}
		if keep {
			out = append(out, qd)
			x.hold(docBytes(qd))
		}
	}
	return out, len(docs), nil
}

// scanStream is one scan's documents.
type scanStream struct {
	x     *executor
	r     *scanReader
	check *query.Recheck
	cands []candidate
	buf   []*query.Doc
}

func (s *scanStream) next(ctx context.Context) (*query.Doc, error) {
	for len(s.buf) == 0 {
		if len(s.cands) == 0 {
			if _, err := s.r.head(ctx); err != nil {
				return nil, err
			}
			for _, h := range s.r.buf {
				s.cands = append(s.cands, candidate{h.Path, s.r.gen})
			}
			s.r.buf = nil
			if len(s.cands) == 0 {
				return nil, nil
			}
		}
		docs, n, err := s.x.fetch(ctx, s.cands, s.check)
		if err != nil {
			return nil, err
		}
		s.cands, s.buf = s.cands[n:], docs
	}
	d := s.buf[0]
	s.buf = s.buf[1:]
	return d, nil
}

// mergedStream intersects equality scans by name, each in name order (index_merged): a
// zig-zag join that seeks every scan to the furthest name any of them is at.
type mergedStream struct {
	x     *executor
	scans []*scanReader
	check *query.Recheck
	cands []candidate
	buf   []*query.Doc
	done  bool
}

func (m *mergedStream) next(ctx context.Context) (*query.Doc, error) {
	for len(m.buf) == 0 {
		if len(m.cands) == 0 {
			if m.done {
				return nil, nil
			}
			for len(m.cands) < m.x.page && !m.done {
				c, err := m.join(ctx)
				if err != nil {
					return nil, err
				}
				if c == nil {
					m.done = true
					break
				}
				m.cands = append(m.cands, *c)
			}
			continue
		}
		docs, n, err := m.x.fetch(ctx, m.cands, m.check)
		if err != nil {
			return nil, err
		}
		m.cands, m.buf = m.cands[n:], docs
	}
	d := m.buf[0]
	m.buf = m.buf[1:]
	return d, nil
}

// join returns the next document every scan holds, nil past the last: with the latest batch
// that read one of its entries.
func (m *mergedStream) join(ctx context.Context) (*candidate, error) {
	var target []byte
	for {
		agreed := 0
		for _, r := range m.scans {
			if target != nil {
				r.seek(target)
			}
			h, err := r.head(ctx)
			if err != nil || h == nil {
				return nil, err
			}
			switch suf := r.suffix(h); {
			case target == nil || bytes.Equal(suf, target):
				target = suf
				agreed++
			default:
				// Past the target: every scan seeks to this one.
				target = suf
				agreed = 1
			}
		}
		if agreed == len(m.scans) {
			h, _ := m.scans[0].head(ctx)
			c := &candidate{path: h.Path}
			for _, r := range m.scans {
				c.gen = max(c.gen, r.gen)
				r.take()
			}
			return c, nil
		}
	}
}

// changedStream is the documents that changed since the read time, as they were then, after
// the last result the union returned, in the query's order.
type changedStream struct{ docs []*query.Doc }

func (c *changedStream) next(context.Context) (*query.Doc, error) {
	if len(c.docs) == 0 {
		return nil, nil
	}
	d := c.docs[0]
	c.docs = c.docs[1:]
	return d, nil
}

// unionStream merges streams in the query's order, each document once: the plan's streams (an
// OR, an in or an array-contains-any has several) and, last, the documents that changed. A
// document two streams return comes with the same values in both, at the read time, so the
// copies are adjacent.
type unionStream struct {
	x       *executor
	streams []stream
	changed *changedStream
	heads   []*query.Doc
	started []bool
	last    *query.Doc
}

func (u *unionStream) next(ctx context.Context) (*query.Doc, error) {
	q := u.x.q
	for {
		best := -1
		for i, st := range u.streams {
			if !u.started[i] {
				d, err := st.next(ctx)
				if err != nil {
					return nil, err
				}
				u.heads[i], u.started[i] = d, true
			}
			if u.heads[i] != nil && (best < 0 || q.Compare(u.heads[i], u.heads[best]) < 0) {
				best = i
			}
		}
		if best < 0 {
			return nil, nil
		}
		d := u.heads[best]
		u.started[best] = false
		if best == len(u.streams)-1 {
			u.x.changedBytes -= docBytes(d)
		}
		if u.last != nil && u.last.Name == d.Name {
			u.x.hold(-docBytes(d))
			continue // the document of another stream already returned
		}
		u.last = d
		return d, nil
	}
}

// addChanged adds documents that changed, at the read time, to those the union merges: those
// after the last result it returned.
func (u *unionStream) addChanged(docs []*query.Doc) error {
	if len(docs) == 0 {
		return nil
	}
	q, i := u.x.q, len(u.streams)-1
	if u.started[i] && u.heads[i] != nil {
		u.changed.docs = append(u.changed.docs, u.heads[i])
	}
	u.heads[i], u.started[i] = nil, false
	for _, d := range docs {
		if u.last != nil && q.Compare(d, u.last) <= 0 {
			continue
		}
		u.changed.docs = append(u.changed.docs, d)
		n := docBytes(d)
		u.x.hold(n)
		if u.x.changedBytes += n; u.x.changedBytes > maxChangedBytes {
			return errChangedBound
		}
	}
	slices.SortFunc(u.changed.docs, q.Compare)
	return nil
}
