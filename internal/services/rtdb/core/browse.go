package core

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/rtdb/queries"
)

// The console's data browser reads the stored rows directly instead of loading subtrees
// through the writer: a page of a node's children costs a few index probes per child, however
// many children and descendants the node has. Reads see committed data only; writes from the
// console still go through a session and the writer (Session.Set and Session.Update).

// Shallow is one page of a node, as REST's shallow=true shows it: the node's value when it is
// a leaf, else its children by key, with the value of those that are leaves.
type Shallow struct {
	// Value is the node itself when it is a leaf; nil for an object or a missing node.
	Value *Node
	// Children are the page's children: those whose keys follow the page's start, in byte
	// order of the keys. A page that holds every child is in Firebase's key order instead
	// (CompareKeys), as a client would see the node.
	Children []ShallowChild
	// Next is the key the next page starts after; empty on the last page.
	Next string
}

// ShallowChild is one child in a Shallow page.
type ShallowChild struct {
	Key string
	// Leaf is the child's value when it is a leaf; nil when it has children of its own.
	Leaf *Node
}

// Size is how much a subtree stores, counted up to a limit.
type Size struct {
	Rows  int // the rows stored: leaves, and objects carrying a priority
	Bytes int64
	// Capped is set when the count stopped at the limit: the subtree is at least this big.
	Capped bool
}

// browser is the read access of the data browser. PGStore implements it; a store without it
// leaves the browser unavailable.
type browser interface {
	shallow(ctx context.Context, db DatabaseInfo, p Path, after string, limit int) (Shallow, error)
	size(ctx context.Context, db DatabaseInfo, p Path, maxRows int) (Size, error)
}

// MaxShallowPage caps the children of one Shallow page.
const MaxShallowPage = 1000

// Shallow reads one page of the node at path: its value if it is a leaf, else up to limit of
// its children whose keys follow after (all of them from the first when after is empty).
func (s *Service) Shallow(ctx context.Context, t Target, path, after string, limit int) (Shallow, error) {
	p, err := ParsePath(path)
	if err != nil {
		return Shallow{}, err
	}
	b, ok := s.store.(browser)
	if !ok {
		return Shallow{}, ErrUnavailable
	}
	db, err := s.store.OpenDatabase(ctx, t.Project, t.Name)
	if err != nil {
		s.log.Error("rtdb: browse", "database", t.Name, "error", err)
		return Shallow{}, ErrUnavailable
	}
	page, err := b.shallow(ctx, db, p, after, min(max(limit, 1), MaxShallowPage))
	if err != nil {
		s.log.Error("rtdb: browse", "database", t.Name, "error", err)
		return Shallow{}, ErrUnavailable
	}
	return page, nil
}

// SizeOf counts the rows and value bytes stored at path and below, stopping at maxRows rows.
func (s *Service) SizeOf(ctx context.Context, t Target, path string, maxRows int) (Size, error) {
	p, err := ParsePath(path)
	if err != nil {
		return Size{}, err
	}
	b, ok := s.store.(browser)
	if !ok {
		return Size{}, ErrUnavailable
	}
	db, err := s.store.OpenDatabase(ctx, t.Project, t.Name)
	if err != nil {
		s.log.Error("rtdb: size", "database", t.Name, "error", err)
		return Size{}, ErrUnavailable
	}
	size, err := b.size(ctx, db, p, maxRows)
	if err != nil {
		s.log.Error("rtdb: size", "database", t.Name, "error", err)
		return Size{}, ErrUnavailable
	}
	return size, nil
}

// WriteCount returns how many writes a database has applied.
func (s *Service) WriteCount(ctx context.Context, t Target) (int64, error) {
	db, err := s.store.OpenDatabase(ctx, t.Project, t.Name)
	if err != nil {
		s.log.Error("rtdb: write count", "database", t.Name, "error", err)
		return 0, ErrUnavailable
	}
	seq, err := s.store.Seq(ctx, db)
	if err != nil {
		s.log.Error("rtdb: write count", "database", t.Name, "error", err)
		return 0, ErrUnavailable
	}
	return seq, nil
}

func (s *PGStore) size(ctx context.Context, db DatabaseInfo, p Path, maxRows int) (Size, error) {
	limit := int32(min(maxRows, 1<<30)) + 1
	var rows, bytes int64
	if p.IsRoot() {
		r, err := s.q.DatabaseSize(ctx, queries.DatabaseSizeParams{DatabaseID: db.ID, MaxRows: limit})
		if err != nil {
			return Size{}, err
		}
		rows, bytes = r.Rows, r.Bytes
	} else {
		lo, hi := bounds(p)
		r, err := s.q.SubtreeSize(ctx, queries.SubtreeSizeParams{DatabaseID: db.ID, Path: p.String(), Lo: lo, Hi: hi, MaxRows: limit})
		if err != nil {
			return Size{}, err
		}
		rows, bytes = r.Rows, r.Bytes
	}
	if rows > int64(maxRows) {
		return Size{Rows: maxRows, Bytes: bytes, Capped: true}, nil
	}
	return Size{Rows: int(rows), Bytes: bytes}, nil
}

// shallow walks the children of p in a read-only snapshot. Rows are ordered by path, which is
// not quite the order of the children's keys: '/' (0x2F) follows bytes such as ' ', '-' and
// '!' that keys may hold, so the rows of an object "a" (a/x) come after those of a key "a-b".
// nextChild finds each following key in byte order regardless (see there).
func (s *PGStore) shallow(ctx context.Context, db DatabaseInfo, p Path, after string, limit int) (Shallow, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Shallow{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	w := &walker{ctx: ctx, q: s.q.WithTx(tx), db: db.ID}
	if !p.IsRoot() {
		w.base = p.String() + "/"
		w.hi = p.String() + "0"
	}

	var out Shallow
	own, err := w.q.ReadRows(ctx, queries.ReadRowsParams{DatabaseID: db.ID, Paths: []string{p.String()}})
	if err != nil {
		return Shallow{}, err
	}
	if len(own) == 1 && own[0].Value != nil {
		if out.Value, err = leafOf(own[0].Value); err != nil {
			return Shallow{}, err
		}
		return out, nil
	}

	var keys []string
	key, started := after, after != ""
	for len(keys) <= limit {
		next, ok, err := w.nextChild(key, started)
		if err != nil {
			return Shallow{}, err
		}
		if !ok {
			break
		}
		keys = append(keys, next)
		key, started = next, true
	}
	if len(keys) > limit {
		keys = keys[:limit]
		out.Next = keys[limit-1]
	}

	paths := make([]string, len(keys))
	for i, k := range keys {
		paths[i] = w.base + k
	}
	rows, err := w.q.ReadRows(ctx, queries.ReadRowsParams{DatabaseID: db.ID, Paths: paths})
	if err != nil {
		return Shallow{}, err
	}
	leaves := make(map[string]*Node, len(rows))
	for _, r := range rows {
		if r.Value == nil {
			continue // an object's priority
		}
		n, err := leafOf(r.Value)
		if err != nil {
			return Shallow{}, err
		}
		leaves[r.Path[len(w.base):]] = n
	}
	out.Children = make([]ShallowChild, len(keys))
	for i, k := range keys {
		out.Children[i] = ShallowChild{Key: k, Leaf: leaves[k]}
	}
	if after == "" && out.Next == "" {
		slices.SortFunc(out.Children, func(a, b ShallowChild) int { return CompareKeys(a.Key, b.Key) })
	}
	return out, tx.Commit(ctx)
}

// walker finds the children of one node among the stored rows: those under base, below hi
// ("" for the root, whose rows have no upper bound).
type walker struct {
	ctx      context.Context
	q        *queries.Queries
	db       int64
	base, hi string
}

// first returns the first row in [lo, hi), or false.
func (w *walker) first(lo, hi string) (string, bool, error) {
	var path string
	var err error
	if hi == "" {
		path, err = w.q.FirstPathFrom(w.ctx, queries.FirstPathFromParams{DatabaseID: w.db, Lo: lo})
	} else {
		if lo >= hi {
			return "", false, nil
		}
		path, err = w.q.FirstPathIn(w.ctx, queries.FirstPathInParams{DatabaseID: w.db, Lo: lo, Hi: hi})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return path, err == nil, err
}

// hasRowsBelow reports whether the child key has rows of its own children.
func (w *walker) hasRowsBelow(key string) (bool, error) {
	return w.q.HasRowsIn(w.ctx, queries.HasRowsInParams{DatabaseID: w.db, Lo: w.base + key + "/", Hi: w.base + key + "0"})
}

// keyOf is the child key a row under base belongs to.
func (w *walker) keyOf(path string) string {
	k, _, _ := strings.Cut(path[len(w.base):], "/")
	return k
}

// nextChild returns the smallest child key, in byte order, that is greater than after (any
// key when started is false).
//
// The rows of a key k are base+k and base+k+"/…". Every key greater than after has its rows
// after base+after and outside after's own subtree, so the first row P of that region names a
// candidate kP. Two things make path order differ from key order, both from bytes below '/':
//   - A proper prefix k of after, with after[len(k)] < '/', is a smaller key whose subtree
//     base+k+"/…" still sorts after base+after; such subtrees are skipped.
//   - A key m smaller than kP can have its first row after P only if m is a proper prefix of
//     kP with kP[len(m)] < '/' and m has no row of its own: m is then the answer when it has
//     children, the shortest such m first.
func (w *walker) nextChild(after string, started bool) (string, bool, error) {
	var cands []string
	if !started {
		lo := w.base
		if lo == "" {
			lo = "\x01" // past the root's own row, ""
		}
		p, ok, err := w.first(lo, w.hi)
		if err != nil || !ok {
			return "", false, err
		}
		cands = append(cands, p)
	} else {
		at := w.base + after
		// Keys extending after with a byte below '/': (at, at+"/"). "\x01" sorts before every
		// byte a key may hold, so at+"\x01" is the first path after at.
		if p, ok, err := w.first(at+"\x01", at+"/"); err != nil {
			return "", false, err
		} else if ok {
			cands = append(cands, p)
		}
		// Every other greater key: from at+"0", past the subtrees of after's prefixes.
		lo := at + "0"
		for {
			p, ok, err := w.first(lo, w.hi)
			if err != nil {
				return "", false, err
			}
			if !ok {
				break
			}
			if k := w.keyOf(p); k < after && strings.HasPrefix(after, k) {
				lo = w.base + k + "0"
				continue
			}
			cands = append(cands, p)
			break
		}
	}
	if len(cands) == 0 {
		return "", false, nil
	}
	kP := w.keyOf(slices.Min(cands))
	for i := 1; i < len(kP); i++ {
		if m := kP[:i]; kP[i] < '/' && (!started || m > after) {
			ok, err := w.hasRowsBelow(m)
			if err != nil {
				return "", false, err
			}
			if ok {
				return m, true, nil
			}
		}
	}
	return kP, true, nil
}
