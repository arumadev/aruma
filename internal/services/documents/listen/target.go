package listen

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// index is a database's targets by where their documents live: a commit's change is judged
// for the targets of its collection id (collections and collection groups), the kindless
// ones, and those that name its document.
type index struct {
	byCollection map[string]map[*Target]struct{}
	kindless     map[*Target]struct{}
	byDocument   map[string]map[*Target]struct{}
}

func newIndex() index {
	return index{byCollection: map[string]map[*Target]struct{}{}, kindless: map[*Target]struct{}{},
		byDocument: map[string]map[*Target]struct{}{}}
}

func addTo(m map[string]map[*Target]struct{}, k string, t *Target) {
	if m[k] == nil {
		m[k] = map[*Target]struct{}{}
	}
	m[k][t] = struct{}{}
}

func removeFrom(m map[string]map[*Target]struct{}, k string, t *Target) {
	delete(m[k], t)
	if len(m[k]) == 0 {
		delete(m, k)
	}
}

func (x index) add(t *Target) {
	switch {
	case t.Query == nil:
		for p := range t.docs {
			addTo(x.byDocument, p, t)
		}
	case t.Query.CollectionID == "":
		x.kindless[t] = struct{}{}
	default:
		addTo(x.byCollection, t.Query.CollectionID, t)
	}
}

func (x index) remove(t *Target) {
	switch {
	case t.Query == nil:
		for p := range t.docs {
			removeFrom(x.byDocument, p, t)
		}
	case t.Query.CollectionID == "":
		delete(x.kindless, t)
	default:
		removeFrom(x.byCollection, t.Query.CollectionID, t)
	}
}

// candidates yields the targets a change of the document at p is judged for.
func (x index) candidates(p core.Path) iter.Seq[*Target] {
	return func(yield func(*Target) bool) {
		for _, set := range []map[*Target]struct{}{x.byCollection[p.CollectionID()], x.kindless, x.byDocument[p.String()]} {
			for t := range set {
				if !yield(t) {
					return
				}
			}
		}
	}
}

// inScope reports whether the document at p is one the target reads.
func (t *Target) inScope(p core.Path) bool {
	if t.Query == nil {
		return t.docs[p.String()]
	}
	q := t.Query
	if q.CollectionID != "" && p.CollectionID() != q.CollectionID {
		return false
	}
	if !q.AllDescendants {
		return slices.Equal(p.Parent(), t.Parent)
	}
	return len(p) > len(t.Parent) && slices.Equal(p[:len(t.Parent)], t.Parent)
}

// admits reports whether d, which exists, is in the target, but for a query's offset and limit.
func (t *Target) admits(d core.Document) bool {
	if !t.inScope(d.Path) {
		return false
	}
	if t.Query == nil {
		return true
	}
	return t.Query.Admits(&query.Doc{Name: t.sub.db.DocumentName(d.Path), Fields: d.Fields,
		CreateTime: d.CreateTime, UpdateTime: d.UpdateTime})
}

// classify judges a change of one document by its images before and after (ADR-026 §2); 0 is
// nothing for the target.
func (t *Target) classify(before, after core.Document) Kind {
	was := before.Exists() && t.admits(before)
	is := after.Exists() && t.admits(after)
	switch {
	case is && !was:
		return Enter
	case is:
		return Modify
	case was && after.Exists():
		return LeaveExists
	case was:
		return LeaveDeleted
	}
	return 0
}

// errSnapshotRows is a target's read past SnapshotRows documents (ADR-026 §3): our own text.
func errSnapshotRows(n int) error {
	return wire.NewError(wire.CodeResourceExhausted,
		fmt.Sprintf("A listen target reads at most %d documents on this server; narrow the query.", n))
}

// ReadAt reads the target's result at the commit at, without changing what it takes: a limit
// window's refill at an event's commit (ADR-026 §1.7) or a resumed target's current result.
func (t *Target) ReadAt(ctx context.Context, at Head, each func(core.Document) error) error {
	s := t.sub
	if t.Query != nil {
		return ReadQuery(ctx, s.d.reader, s.db, t.Parent, t.Query, at, s.h.o.SnapshotRows, each)
	}
	docs, _, err := s.d.reader.BatchGet(ctx, s.db, t.Documents, core.ReadOptions{ReadTime: at.ReadTime})
	if err != nil {
		return err
	}
	for _, d := range docs {
		if d.Exists() {
			if err := each(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadQuery reads q's result under parent at the commit at, through the reference path
// (query.Runner) over the scope's documents, read in pages at at.ReadTime: each gets the results
// in q's order, within its offset and limit. Past maxRows documents read it fails with a
// RESOURCE_EXHAUSTED *wire.Error. A2 reads a limit window's refill with it, q changed as it
// needs.
func ReadQuery(ctx context.Context, r Reader, db core.DB, parent core.Path, q *query.Query, at Head, maxRows int,
	each func(core.Document) error) error {
	prefix := db.DocumentName(nil) + "/"
	run := query.NewRunner(q, false, query.Limits{MaxScanned: maxRows}, func(d *query.Doc) error {
		return each(core.Document{Path: core.ParsePath(strings.TrimPrefix(d.Name, prefix)), Fields: d.Fields,
			CreateTime: d.CreateTime, UpdateTime: d.UpdateTime})
	})
	req := core.ScanRequest{Parent: parent, CollectionID: q.CollectionID, AllDescendants: q.AllDescendants,
		ReadOptions: core.ReadOptions{ReadTime: at.ReadTime}}
	read := 0
	for {
		res, err := r.Scan(ctx, db, req)
		if err != nil {
			return err
		}
		for _, d := range res.Documents {
			if read++; read > maxRows {
				return errSnapshotRows(maxRows)
			}
			done, err := run.Add(&query.Doc{Name: db.DocumentName(d.Path), Fields: d.Fields,
				CreateTime: d.CreateTime, UpdateTime: d.UpdateTime})
			if err != nil {
				return err
			}
			if done {
				return run.Finish()
			}
		}
		if res.Next == nil {
			return run.Finish()
		}
		req.From = res.Next
	}
}

// Changes calls each with the target's changes from the commit since to upto, its snapshot
// (ADR-026 §1.5): every document that entered, changed in, or left its result, classified as
// events are, by filter alone. since.Seq is the commit's seq when known (a resume token's), 0
// otherwise. inWindow is false, and each is not called, when the changes are no longer all held
// (the changelog's retention or the read_time window), or the target's scope is kindless: the
// caller answers with the count or RESET instead.
func (t *Target) Changes(ctx context.Context, since, upto Head, each func(TargetChange) error) (inWindow bool, err error) {
	s := t.sub
	if t.Query == nil {
		o := core.ReadOptions{ReadTime: since.ReadTime}
		before, _, err := s.d.reader.BatchGet(ctx, s.db, t.Documents, o)
		if errors.Is(err, core.ErrReadTimeTooOld) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		after, _, err := s.d.reader.BatchGet(ctx, s.db, t.Documents, core.ReadOptions{ReadTime: upto.ReadTime})
		if err != nil {
			return false, err
		}
		seen := map[string]bool{}
		for i := range before {
			b, a := before[i], after[i]
			if seen[b.Path.String()] || b.UpdateTime.Equal(a.UpdateTime) {
				continue
			}
			seen[b.Path.String()] = true
			if k := t.classify(b, a); k != 0 {
				if err := each(TargetChange{Target: t, Kind: k, Before: b, After: a}); err != nil {
					return false, err
				}
			}
		}
		return true, nil
	}
	q := t.Query
	if q.CollectionID == "" {
		return false, nil
	}
	scope := core.ChangeScope{Parent: t.Parent, CollectionID: q.CollectionID, Group: q.AllDescendants}
	return s.d.reader.ChangesSince(ctx, s.db, core.ChangesRequest{Since: since.ReadTime, SinceSeq: since.Seq, Upto: upto.Seq, Scope: scope},
		func(b, a core.Document) error {
			if !t.inScope(a.Path) {
				return nil
			}
			if k := t.classify(b, a); k != 0 {
				return each(TargetChange{Target: t, Kind: k, Before: b, After: a})
			}
			return nil
		})
}
