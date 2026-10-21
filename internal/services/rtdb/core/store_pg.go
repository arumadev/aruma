package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/rtdb/queries"
)

// PGStore is the Store on Postgres, schema rtdb (ADR-019).
type PGStore struct {
	pool *pgxpool.Pool
	q    *queries.Queries
}

// NewPGStore returns the Postgres store.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, q: queries.New(pool)}
}

// OpenDatabase implements Store.
func (s *PGStore) OpenDatabase(ctx context.Context, project int64, name string) (DatabaseInfo, error) {
	row, err := s.q.EnsureDatabase(ctx, queries.EnsureDatabaseParams{ProjectNumber: project, Name: name})
	if err != nil {
		return DatabaseInfo{}, err
	}
	return DatabaseInfo{ID: row.ID, Project: project, Name: name, Seq: row.Seq, Rules: row.Rules}, nil
}

// Begin implements Store.
func (s *PGStore) Begin(ctx context.Context, db DatabaseInfo) (StoreTx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgTx{tx: tx, q: s.q.WithTx(tx), db: db}, nil
}

// Seq implements Store.
func (s *PGStore) Seq(ctx context.Context, db DatabaseInfo) (int64, error) {
	return s.q.GetSeq(ctx, queries.GetSeqParams{ID: db.ID, ProjectNumber: db.Project})
}

// DeleteProject implements Store.
func (s *PGStore) DeleteProject(ctx context.Context, project int64) error {
	return s.q.DeleteProjectDatabases(ctx, project)
}

// SetRules implements Store.
func (s *PGStore) SetRules(ctx context.Context, project int64, name, doc string) error {
	return s.q.SetRules(ctx, queries.SetRulesParams{ProjectNumber: project, Name: name, Rules: doc})
}

// dataError marks the errors of a value Postgres refuses (SQLSTATE class 22, data
// exception), so the writer fails that write alone.
func dataError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && strings.HasPrefix(pg.Code, "22") {
		return &DataError{Err: err}
	}
	return err
}

type pgTx struct {
	tx pgx.Tx
	q  *queries.Queries
	db DatabaseInfo
}

// bounds returns the range of the rows strictly below p. The root has none: its subtree is
// every row.
func bounds(p Path) (lo, hi string) {
	s := p.String()
	return s + "/", s + "0"
}

func (t *pgTx) Read(ctx context.Context, p Path) (*Node, error) {
	if p.IsPriority() {
		n, err := t.Read(ctx, p.Parent())
		return n.Priority(), err
	}
	var rows []row
	if p.IsRoot() {
		rs, err := t.q.ReadAll(ctx, t.db.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.Path, r.Value, r.Priority})
		}
	} else {
		lo, hi := bounds(p)
		rs, err := t.q.ReadSubtree(ctx, queries.ReadSubtreeParams{DatabaseID: t.db.ID, Path: p.String(), Lo: lo, Hi: hi})
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.Path, r.Value, r.Priority})
		}
	}
	return treeOf(p, rows)
}

func (t *pgTx) Peek(ctx context.Context, p Path) (*Node, bool, error) {
	rs, err := t.q.ReadRows(ctx, queries.ReadRowsParams{DatabaseID: t.db.ID, Paths: []string{p.String()}})
	if err != nil {
		return nil, false, err
	}
	if len(rs) == 1 && rs[0].Value != nil {
		leaf, err := leafOf(rs[0].Value)
		return leaf, false, err
	}
	// No row, or an object's priority row: children are what remains. Below a leaf there are
	// no rows, so a path under a leaf holds nothing.
	has, err := t.hasRowsBelow(ctx, p)
	return nil, has, err
}

func (t *pgTx) HasChildExcept(ctx context.Context, p Path, keys []string) (bool, error) {
	arg := queries.HasRowsOutsideParams{DatabaseID: t.db.ID, Keys: keys}
	if keys == nil {
		arg.Keys = []string{}
	}
	if !p.IsRoot() {
		arg.Lo, arg.Hi = bounds(p)
		arg.Bounded = true
	}
	arg.Skip = int32(utf8.RuneCountInString(arg.Lo) + 1) // substr counts characters from 1
	return t.q.HasRowsOutside(ctx, arg)
}

// ancestors returns the rows stored at p's proper ancestors, the root first.
func (t *pgTx) ancestors(ctx context.Context, p Path) ([]row, error) {
	paths := make([]string, len(p))
	for i := range p {
		paths[i] = p[:i].String()
	}
	rs, err := t.q.ReadRows(ctx, queries.ReadRowsParams{DatabaseID: t.db.ID, Paths: paths})
	if err != nil {
		return nil, err
	}
	out := make([]row, 0, len(rs))
	for _, r := range rs {
		out = append(out, row{r.Path, r.Value, r.Priority})
	}
	slices.SortFunc(out, func(a, b row) int { return len(a.Path) - len(b.Path) })
	return out, nil
}

func (t *pgTx) Write(ctx context.Context, p Path, v *Node) error {
	return dataError(t.write(ctx, p, v))
}

func (t *pgTx) write(ctx context.Context, p Path, v *Node) error {
	if p.IsPriority() {
		return t.writePriority(ctx, p.Parent(), v)
	}
	anc, err := t.ancestors(ctx, p)
	if err != nil {
		return err
	}
	// At most one ancestor is a leaf: writing below a leaf turns it into an object.
	for _, a := range anc {
		if a.Value == nil {
			continue
		}
		if v == nil {
			return nil // deleting below a leaf changes nothing
		}
		if err := t.leafToObject(ctx, a); err != nil {
			return err
		}
	}
	if err := t.deleteSubtree(ctx, p); err != nil {
		return err
	}
	if v != nil {
		return t.insert(ctx, rowsOf(p, v))
	}
	// An object left without children is null, and its priority goes with it.
	for i := len(anc) - 1; i >= 0; i-- {
		a := anc[i]
		if a.Value != nil {
			continue
		}
		path, err := ParsePath(a.Path)
		if err != nil {
			return err
		}
		has, err := t.hasRowsBelow(ctx, path)
		if err != nil || has {
			return err
		}
		if err := t.q.DeleteRow(ctx, queries.DeleteRowParams{DatabaseID: t.db.ID, Path: a.Path}); err != nil {
			return err
		}
	}
	return nil
}

// leafToObject keeps a leaf's priority on the object that replaces it (LeafNode.updateChild).
func (t *pgTx) leafToObject(ctx context.Context, r row) error {
	if r.Priority == nil {
		return t.q.DeleteRow(ctx, queries.DeleteRowParams{DatabaseID: t.db.ID, Path: r.Path})
	}
	return t.q.UpdateRow(ctx, queries.UpdateRowParams{DatabaseID: t.db.ID, Path: r.Path, Value: nil, Priority: r.Priority})
}

// writePriority sets or clears the priority of the node at p. Null has no priority, so a
// missing node stays missing (EMPTY_NODE.updatePriority).
func (t *pgTx) writePriority(ctx context.Context, p Path, prio *Node) error {
	if prio != nil && !prio.IsLeaf() {
		return ErrInvalidPriority
	}
	rs, err := t.q.ReadRows(ctx, queries.ReadRowsParams{DatabaseID: t.db.ID, Paths: []string{p.String()}})
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if prio != nil {
		raw = storedLeaf(prio)
	}
	if len(rs) == 1 {
		r := rs[0]
		if r.Value == nil && raw == nil {
			return t.q.DeleteRow(ctx, queries.DeleteRowParams{DatabaseID: t.db.ID, Path: r.Path})
		}
		return t.q.UpdateRow(ctx, queries.UpdateRowParams{DatabaseID: t.db.ID, Path: r.Path, Value: r.Value, Priority: raw})
	}
	if raw == nil {
		return nil
	}
	has, err := t.hasRowsBelow(ctx, p)
	if err != nil || !has {
		return err
	}
	return t.q.SetPriority(ctx, queries.SetPriorityParams{DatabaseID: t.db.ID, Path: p.String(), Priority: raw})
}

func (t *pgTx) hasRowsBelow(ctx context.Context, p Path) (bool, error) {
	if p.IsRoot() {
		return t.q.HasRowsBelowRoot(ctx, t.db.ID)
	}
	lo, hi := bounds(p)
	return t.q.HasRowsIn(ctx, queries.HasRowsInParams{DatabaseID: t.db.ID, Lo: lo, Hi: hi})
}

func (t *pgTx) deleteSubtree(ctx context.Context, p Path) error {
	if p.IsRoot() {
		return t.q.DeleteAll(ctx, t.db.ID)
	}
	lo, hi := bounds(p)
	return t.q.DeleteSubtree(ctx, queries.DeleteSubtreeParams{DatabaseID: t.db.ID, Path: p.String(), Lo: lo, Hi: hi})
}

func (t *pgTx) insert(ctx context.Context, rows []row) error {
	if len(rows) == 0 {
		return nil
	}
	params := make([]queries.InsertNodesParams, len(rows))
	for i, r := range rows {
		params[i] = queries.InsertNodesParams{DatabaseID: t.db.ID, Path: r.Path, Value: r.Value, Priority: r.Priority}
	}
	_, err := t.q.InsertNodes(ctx, params)
	return err
}

func (t *pgTx) AdvanceSeq(ctx context.Context, n int64) (int64, error) {
	return t.q.AdvanceSeq(ctx, queries.AdvanceSeqParams{ID: t.db.ID, ProjectNumber: t.db.Project, N: n})
}

func (t *pgTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
