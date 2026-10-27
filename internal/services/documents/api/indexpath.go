package api

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The index paths (ADR-025 §1): a query the planner serves exactly (query.Plan) reads index
// entries in place of the reference path's candidates, in one snapshot of the database
// (core.IndexRead), every candidate checked again by the reference matcher (query.Recheck) and
// then run by the reference runner in the order the entries come. A read at a read_time or in a
// transaction stays on the reference path: entries hold the latest state alone, and a
// transaction's reads take its locks and its record of reads on the reference path's scope.

// firstPage and maxPage bound the entries a scan reads at a time: the first page is what the
// offset and the limit want, and each page after it doubles, up to maxPage.
const (
	firstPage = 100
	maxPage   = 1000
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
	defer rd.Close(ctx)
	plan, ok := q.Plan(rd.Catalog())
	if !ok {
		return 0, time.Time{}, false, nil
	}
	x := &executor{rd: rd, a: a, parent: parent, q: q, page: firstPage}
	if q.Limit > 0 {
		x.page = min(max(q.Offset+q.Limit, 1), maxPage)
	}
	st, ok := x.plan(plan)
	if !ok {
		return 0, time.Time{}, false, nil
	}
	readTime = rd.ReadTime()
	var runner *query.Runner
	runner = query.NewOrderedRunner(q, func(d *query.Doc) error { return emit(d, readTime, runner.Skipped()) })
	err = func() error {
		for {
			d, err := st.next(ctx)
			if err != nil || d == nil {
				return err
			}
			if done, err := runner.Add(d); err != nil || done {
				return err
			}
		}
	}()
	if err == nil {
		err = runner.Finish()
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
	return runner.Skipped(), readTime, true, nil
}

// executor builds and reads a plan's streams in one index read.
type executor struct {
	rd     *core.IndexRead
	a      admitted
	parent wire.Name
	q      *query.Query
	page   int
}

// stream is candidates in the query's order; next returns nil past the last.
type stream interface {
	next(ctx context.Context) (*query.Doc, error)
}

// plan returns the plan's candidates: its one stream, or its streams' union. ok is false when an
// index the plan names is not in the read's catalogue.
func (x *executor) plan(p *query.Plan) (stream, bool) {
	var streams []stream
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
		check := x.q.Recheck(st)
		if len(scans) == 1 {
			streams = append(streams, &scanStream{x: x, r: scans[0], check: check})
		} else {
			streams = append(streams, &mergedStream{x: x, scans: scans, check: check})
		}
	}
	if len(streams) == 1 {
		return streams[0], true
	}
	return &unionStream{q: x.q, streams: streams, heads: make([]*query.Doc, len(streams)), started: make([]bool, len(streams))}, true
}

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
	r := &scanReader{x: x, id: id, base: base, lo: base, hi: successor(base)}
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

// scanReader reads one scan's entries page by page.
type scanReader struct {
	x      *executor
	id     int64
	base   []byte
	lo, hi []byte
	page   int
	buf    []core.IndexHit
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
		r.page = min(2*r.page, maxPage)
		r.buf = hits
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

// documents reads the documents of hits, checked by check, in order.
func (x *executor) documents(ctx context.Context, paths []core.Path, check *query.Recheck) ([]*query.Doc, error) {
	docs, err := x.rd.Documents(ctx, paths)
	if err != nil {
		return nil, err
	}
	out := make([]*query.Doc, 0, len(docs))
	for _, d := range docs {
		qd := &query.Doc{Name: x.a.db.DocumentName(d.Path), Fields: d.Fields, CreateTime: d.CreateTime, UpdateTime: d.UpdateTime}
		keep, err := check.Check(qd)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, qd)
		}
	}
	return out, nil
}

// scanStream is one scan's documents.
type scanStream struct {
	x     *executor
	r     *scanReader
	check *query.Recheck
	buf   []*query.Doc
}

func (s *scanStream) next(ctx context.Context) (*query.Doc, error) {
	for len(s.buf) == 0 {
		if _, err := s.r.head(ctx); err != nil {
			return nil, err
		}
		paths := make([]core.Path, len(s.r.buf))
		for i, h := range s.r.buf {
			paths[i] = h.Path
		}
		s.r.buf = nil
		if len(paths) == 0 {
			return nil, nil
		}
		docs, err := s.x.documents(ctx, paths, s.check)
		if err != nil {
			return nil, err
		}
		s.buf = docs
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
	buf   []*query.Doc
	done  bool
}

func (m *mergedStream) next(ctx context.Context) (*query.Doc, error) {
	for len(m.buf) == 0 {
		if m.done {
			return nil, nil
		}
		var paths []core.Path
		for len(paths) < m.x.page && !m.done {
			p, err := m.join(ctx)
			if err != nil {
				return nil, err
			}
			if p == nil {
				m.done = true
				break
			}
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			continue
		}
		docs, err := m.x.documents(ctx, paths, m.check)
		if err != nil {
			return nil, err
		}
		m.buf = docs
	}
	d := m.buf[0]
	m.buf = m.buf[1:]
	return d, nil
}

// join returns the next document every scan holds, nil past the last.
func (m *mergedStream) join(ctx context.Context) (core.Path, error) {
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
			p := h.Path
			for _, r := range m.scans {
				r.take()
			}
			return p, nil
		}
	}
}

// unionStream merges streams in the query's order, each document once (an OR, an in or an
// array-contains-any).
type unionStream struct {
	q       *query.Query
	streams []stream
	heads   []*query.Doc
	started []bool
	last    *query.Doc
}

func (u *unionStream) next(ctx context.Context) (*query.Doc, error) {
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
			if u.heads[i] != nil && (best < 0 || u.q.Compare(u.heads[i], u.heads[best]) < 0) {
				best = i
			}
		}
		if best < 0 {
			return nil, nil
		}
		d := u.heads[best]
		u.started[best] = false
		if u.last != nil && u.last.Name == d.Name {
			continue // the document of another disjunct already sent
		}
		u.last = d
		return d, nil
	}
}
