package core

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// The hub's reads (ADR-024 §4, §5; ADR-026 §2): the last commit, the commits the hub missed as
// the changelog holds them, and the changes a resumed target catches up on. A target's snapshot
// is Scan or BatchGet at the read_time Head returned: a commit is in it when its seq is at most
// Head's, since a later commit's commit_time is later (ADR-024 §2.1), so the pages, each one
// statement, are one snapshot without a transaction held between them.

// LoggedCommit is a commit as the changelog holds it: its changes in name key order.
type LoggedCommit struct {
	Seq        int64
	CommitTime time.Time
	Changes    []Change
}

// loggedCommitsBudget bounds the images LoggedCommits holds: it stops at the end of the commit
// that passes it.
const loggedCommitsBudget = 32 << 20

// Head returns the database's last commit: its seq and commit_time. A missing '(default)' is
// created first.
func (s *PGStore) Head(ctx context.Context, db DB) (int64, time.Time, error) {
	if err := s.serving(); err != nil {
		return 0, time.Time{}, err
	}
	for ensured := false; ; ensured = true {
		h, err := s.q.ReadHead(ctx, queries.ReadHeadParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		switch {
		case err == nil:
			return h.Seq, h.LastTs, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return 0, time.Time{}, err
		case ensured:
			return 0, time.Time{}, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
		}
		if err := s.missing(ctx, db); err != nil {
			return 0, time.Time{}, err
		}
	}
}

// LoggedCommits returns the commits after seq after, up to after+limit and to the last, from the
// changelog in one statement: consecutive from the first, whole, and committed. It stops early,
// at a commit's end, past a budget of their images. head is the last commit's seq and time in
// the same snapshot. When the first commit returned is past after+1, or none is returned while
// head is past after, retention pruned the rows between.
func (s *PGStore) LoggedCommits(ctx context.Context, db DB, after int64, limit int) (head int64, headTime time.Time, out []LoggedCommit, err error) {
	if err := s.serving(); err != nil {
		return 0, time.Time{}, nil, err
	}
	sql, args := statement(func(q *queries.Queries) error {
		_, err := q.LoggedCommits(ctx, queries.LoggedCommitsParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			AfterSeq: after, MaxSeq: after + int64(max(limit, 1))})
		return err
	})
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return 0, time.Time{}, nil, err
	}
	defer rows.Close()
	found, size := false, 0
	for rows.Next() {
		var seq *int64
		var commitTime *time.Time
		var name, before, after []byte
		if err := rows.Scan(&head, &headTime, &seq, &commitTime, &name, &before, &after); err != nil {
			return 0, time.Time{}, nil, err
		}
		found = true
		if seq == nil {
			break
		}
		if n := len(out); n == 0 || out[n-1].Seq != *seq {
			if size > loggedCommitsBudget {
				break
			}
			out = append(out, LoggedCommit{Seq: *seq, CommitTime: *commitTime})
		}
		c, err := loggedChange(name, before, after, *commitTime)
		if err != nil {
			return 0, time.Time{}, nil, err
		}
		size += len(before) + len(after)
		last := &out[len(out)-1]
		last.Changes = append(last.Changes, c)
	}
	if err := rows.Err(); err != nil {
		return 0, time.Time{}, nil, err
	}
	if !found {
		return 0, time.Time{}, nil, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	return head, headTime, out, nil
}

// loggedChange decodes a changelog row: an image NULL is a document that does not exist. The
// image after holds no update_time, which is its commit's, and a create_time only when the
// document existed before (writes.go, logImages).
func loggedChange(name, before, after []byte, commitTime time.Time) (Change, error) {
	p, err := pathOfKey(name)
	if err != nil {
		return Change{}, err
	}
	c := Change{Path: p, Before: Document{Path: p}, After: Document{Path: p}}
	if before != nil {
		if c.Before.Fields, c.Before.CreateTime, c.Before.UpdateTime, err = decodeDocument(before); err != nil {
			return Change{}, err
		}
	}
	if after != nil {
		if c.After.Fields, c.After.CreateTime, _, err = decodeDocument(after); err != nil {
			return Change{}, err
		}
		c.After.UpdateTime = commitTime
		if c.After.CreateTime.IsZero() {
			c.After.CreateTime = commitTime
		}
	}
	return c, nil
}

// ChangesRequest is a ChangesSince call.
type ChangesRequest struct {
	// Since is the commit_time the changes are after: a resumed target's read time.
	Since time.Time
	// SinceSeq, when known (a resume token's), is the seq of the commit at Since, which spares
	// finding the first commit after it.
	SinceSeq int64
	// Upto is the last commit the changes reach: the target's snapshot's seq.
	Upto int64
	// Scope is the documents asked for. Postgres keeps out what is surely not in it; a
	// collection group's comes with every parent.
	Scope ChangeScope
}

// ChangesSince calls each with the documents of r.Scope that the commits after r.Since, up to
// r.Upto, changed (ADR-026 §1.5): each once, in name key order, as it was at r.Since and as it is
// at r.Upto (Path alone where it did not exist). The rows stream one at a time from one
// statement. inWindow is false, and each is not called, when the changelog or the read_time
// window may no longer hold every such change: the caller answers from the current result
// instead.
func (s *PGStore) ChangesSince(ctx context.Context, db DB, r ChangesRequest, each func(before, after Document) error) (inWindow bool, err error) {
	if err := s.serving(); err != nil {
		return false, err
	}
	w, err := s.q.ChangesWindow(ctx, queries.ChangesWindowParams{ProjectNumber: db.Project, DatabaseID: db.ID,
		Since: r.Since, RetentionSecs: s.changelogRetention.Seconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	if err != nil || !w.InWindow {
		return false, err
	}
	after := r.SinceSeq
	if after == 0 {
		if w.FirstSeq == 0 {
			return true, nil // no commit after Since
		}
		after = w.FirstSeq - 1
	}
	if after >= r.Upto {
		return true, nil
	}
	arg := queries.ChangesBetweenParams{ProjectNumber: db.Project, DatabaseID: db.ID, AfterSeq: after, UptoSeq: r.Upto}
	if r.Scope.Group {
		seg := appendSegment(nil, r.Scope.CollectionID)
		slices.Reverse(seg)
		arg.InGroup, arg.Lo, arg.Hi = true, []byte{value.KeyVersion}, []byte{value.KeyVersion + 1}
		arg.GroupNumeric, arg.GroupText = append([]byte{segNumeric}, seg...), append([]byte{segText}, seg...)
	} else {
		arg.Lo = collectionPrefix(r.Scope.Parent, r.Scope.CollectionID)
		arg.Hi, arg.PrefixLen = IndexPrefixEnd(arg.Lo), int32(len(arg.Lo))
	}
	sql, args := statement(func(q *queries.Queries) error {
		_, err := q.ChangesBetween(ctx, arg)
		return err
	})
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, before, after []byte
		var afterTime time.Time
		if err := rows.Scan(&name, &before, &after, &afterTime); err != nil {
			return false, err
		}
		c, err := loggedChange(name, before, after, afterTime)
		if err != nil {
			return false, err
		}
		if !r.Scope.holds(c.Path) {
			continue
		}
		if err := each(c.Before, c.After); err != nil {
			return false, err
		}
	}
	return true, rows.Err()
}
