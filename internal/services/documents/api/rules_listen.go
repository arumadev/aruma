package api

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The rules on a Listen stream (ADR-026 §2), as production applies them (f5-rules-listen.json):
//
//   - A query target is judged as a list (rules_list.go) after ADD, before the database's own
//     refusals; a document target's documents each as a get, with the target's first answer, so
//     what it would have sent is dropped. A refused target is REMOVE with the cause
//     PERMISSION_DENIED "Missing or insufficient permissions." and the stream stays open
//     (listen.doc-denied, listen.query-denied); it is not the stream's, so a remove_target of
//     it ends the stream NOT_FOUND "Target id not found: N" (listen.remove-after-denied).
//   - Before a commit is sent, each document it brings a document target is judged again as a
//     get (resource the document after the commit, null for a delete), and a query target whose
//     rules read another document (get, exists) is judged again as a list: a target refused then
//     is REMOVE with the cause, and nothing of the commit is sent to it.
//   - A target whose rules read another document is also judged again every listenRecheck
//     without a commit of its own: production removed such a target about 2.8 s after the
//     document its rule read changed (listen.query-becomes-denied). Ours is a recheck at a fixed
//     interval, not a watch of the documents read (our choice; production's mechanism is not
//     recorded), and a target refused is never let back in.
//   - Every listenRecheck the stream also resolves the project's release once: every target
//     judged under another ruleset than the one released now is judged again under it, and,
//     while the released ruleset reads request.time (rules.Ruleset.ReadsRequestTime), every
//     target is. Production says a release "can take up to 10 minutes to ... affect any active
//     listeners" (firebase.google.com/docs/firestore/security/get-started); how soon, and how
//     production treats a rule on request.time, are unrecorded (ADR-026 §2). A target still
//     allowed sends nothing more.

// listenRecheck is how often a target whose rules read other documents is judged again, and
// how often the stream looks for a new release.
const listenRecheck = 2 * time.Second

// trackedLookups records whether the rules read a document through them.
type trackedLookups struct {
	rules.Documents
	used bool
}

// Get implements rules.Documents.
func (t *trackedLookups) Get(ctx context.Context, project int64, path []string, after bool) (rules.Value, bool, error) {
	t.used = true
	return t.Documents.Get(ctx, project, path, after)
}

// judgeQuery judges a query target as a list at the latest commit.
func (l *ListenStream) judgeQuery(ctx context.Context, lt *listenTarget) error {
	if l.a.guard == nil {
		return nil
	}
	rs, err := l.s.ruleset(ctx, l.a)
	if err != nil {
		return err
	}
	lt.judgedUnder = rs
	tr := &trackedLookups{Documents: &lookups{store: l.s.o.Store, db: l.a.db, usage: l.s.o.Usage}}
	err = l.s.judgeList(ctx, l.a, rs, listOfPath(lt.parent, lt.q), tr, rules.NewPooledBudget(), l.s.now())
	lt.lookups = lt.lookups || tr.used
	return err
}

// judgeDocs judges a document target's documents as gets, their lookups read at readTime (zero:
// the latest). A document that does not exist has only its path. whole is a judgement of all of
// the target's documents, which records the ruleset it was under.
func (l *ListenStream) judgeDocs(ctx context.Context, lt *listenTarget, docs []core.Document, readTime time.Time, whole bool) error {
	if l.a.guard == nil {
		return nil
	}
	rs, err := l.s.ruleset(ctx, l.a)
	if err != nil {
		return err
	}
	if whole {
		lt.judgedUnder = rs
	}
	tr := &trackedLookups{Documents: &lookups{store: l.s.o.Store, db: l.a.db, readTime: readTime, usage: l.s.o.Usage}}
	budget, now := readBudget(), l.s.now()
	defer func() { lt.lookups = lt.lookups || tr.used }()
	for _, d := range docs {
		op := ruleOp{method: rules.MethodGet, path: d.Path, before: d, now: now, lookups: tr, budget: budget}
		if err := l.s.judge(ctx, l.a, rs, op); err != nil {
			return err
		}
	}
	return nil
}

// judgeFound judges a document target's answer: each of its documents as found at readTime.
func (l *ListenStream) judgeFound(ctx context.Context, lt *listenTarget, found map[string]core.Document, readTime time.Time) error {
	docs := make([]core.Document, len(lt.paths))
	for i, p := range lt.paths {
		d, ok := found[p.String()]
		if !ok {
			d = core.Document{Path: p}
		}
		docs[i] = d
	}
	return l.judgeDocs(ctx, lt, docs, readTime, true)
}

// refuseNew refuses a target not yet the stream's: REMOVE with err as its cause. An error that
// is not a *wire.Error ends the stream.
func (l *ListenStream) refuseNew(ctx context.Context, id int32, err error) error {
	we, ok := errors.AsType[*wire.Error](err)
	if !ok {
		return l.s.fail(ctx, err)
	}
	return l.send(removeTarget(id, we))
}

// recheckEvent judges again, before a commit is sent, the targets it changes: a document
// target's documents as the commit leaves them, a query target whose rules read other documents
// as a whole. A refused target leaves the stream before the commit is sent.
func (l *ListenStream) recheckEvent(ctx context.Context, changes []listen.TargetChange) error {
	if l.a.guard == nil {
		return nil
	}
	seen := map[*listenTarget]bool{}
	for _, c := range changes {
		lt := l.byTarget[c.Target]
		if lt == nil {
			continue // refused at an earlier change of the commit
		}
		var err error
		switch {
		case lt.paths != nil:
			err = l.judgeDocs(ctx, lt, []core.Document{c.After}, time.Time{}, false)
		case lt.lookups && !seen[lt]:
			seen[lt] = true
			err = l.judgeQuery(ctx, lt)
		}
		if err != nil {
			if err := l.targetFailed(lt, err); err != nil {
				return err
			}
		}
	}
	return nil
}

// recheck judges again, at the latest commit, every target whose rules read other documents,
// every target judged under another ruleset than the one released now, and every target while
// that ruleset reads request.time. The release is resolved once; when it cannot be, only the
// targets whose rules read other documents are judged (and fail).
func (l *ListenStream) recheck(ctx context.Context) error {
	rs, rerr := l.s.ruleset(ctx, l.a)
	clock := rerr == nil && rs != nil && rs.ReadsRequestTime()
	for _, id := range slices.Sorted(maps.Keys(l.targets)) {
		lt := l.targets[id]
		if !lt.lookups && !clock && (rerr != nil || lt.judgedUnder == rs) {
			continue
		}
		var err error
		if lt.paths != nil {
			docs := make([]core.Document, len(lt.paths))
			for i, p := range lt.paths {
				d, _, gerr := l.s.o.Store.Get(ctx, l.a.db, p, core.ReadOptions{})
				if ce, ok := errors.AsType[*core.Error](gerr); ok && ce.Code == core.NotFound {
					d, gerr = core.Document{Path: p}, nil
				}
				if gerr != nil {
					return l.s.fail(ctx, gerr)
				}
				docs[i] = d
			}
			err = l.judgeDocs(ctx, lt, docs, time.Time{}, true)
		} else {
			err = l.judgeQuery(ctx, lt)
		}
		if err != nil {
			if err := l.targetFailed(lt, err); err != nil {
				return err
			}
		}
	}
	return nil
}

// recheckTicker is the stream's recheck clock: nil, which never fires, for a caller the rules
// do not hold.
func (l *ListenStream) recheckTicker() (<-chan time.Time, func()) {
	if l.a.guard == nil {
		return nil, func() {}
	}
	t := time.NewTicker(listenRecheck)
	return t.C, t.Stop
}
