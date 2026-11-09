package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Store keeps the databases (ADR-019): one row per leaf, keyed by its path, and one row per
// object that carries a priority. Only a database's writer uses it, so a transaction never
// races another on the same database.
type Store interface {
	// OpenDatabase returns a namespace's database, creating it on first use with the locked
	// rules a new database starts with (ADR-020).
	OpenDatabase(ctx context.Context, project int64, name string) (DatabaseInfo, error)
	// Begin starts a transaction on a database.
	Begin(ctx context.Context, db DatabaseInfo) (StoreTx, error)
	// Seq reads a database's committed write count, outside any transaction: after a commit
	// whose outcome is unknown, it tells whether the commit took effect.
	Seq(ctx context.Context, db DatabaseInfo) (int64, error)
	// DeleteProject removes every database of a project and its data.
	DeleteProject(ctx context.Context, project int64) error
	// SetRules replaces the rules document of a database that exists (OpenDatabase).
	SetRules(ctx context.Context, project int64, name, doc string) error
}

// DataError is a value the storage refused, such as text Postgres cannot hold (SQLSTATE
// class 22). It fails the one write that carried it, not the transaction's other writes.
type DataError struct{ Err error }

func (e *DataError) Error() string { return "rtdb: data refused by storage: " + e.Err.Error() }
func (e *DataError) Unwrap() error { return e.Err }

// DatabaseInfo identifies a stored database.
type DatabaseInfo struct {
	ID      int64
	Project int64
	Name    string
	// Seq is the number of writes applied so far; it grows in the transaction of each write.
	Seq int64
	// Rules is the rules document as stored (locked for a new database, ADR-020).
	Rules string
}

// LockedRules is the rules document a new database starts with (ADR-020), as the migration
// stores it.
const LockedRules = `{"rules": {".read": false, ".write": false}}`

// StoreTx is a transaction on one database. Reads see the transaction's own writes.
type StoreTx interface {
	// Read returns the value at p, or nil.
	Read(ctx context.Context, p Path) (*Node, error)
	// CountRows counts the rows Read(p) would load, up to limit: a count above limit is
	// reported as limit+1, so a read too large is found without counting all of it.
	CountRows(ctx context.Context, p Path, limit int64) (int64, error)
	// Peek tells what is at p without reading below it: the leaf there, or whether p has
	// children. Security rules read the data through it (rulesSource).
	Peek(ctx context.Context, p Path) (leaf *Node, hasChildren bool, err error)
	// HasChildExcept reports whether p has a child whose key is not in keys.
	HasChildExcept(ctx context.Context, p Path, keys []string) (bool, error)
	// Write replaces the value at p with v (nil deletes), as the SDKs' updateChild does: see
	// Node.Set. A path ending in .priority sets the priority of its parent.
	Write(ctx context.Context, p Path, v *Node) error
	// AdvanceSeq adds n to the database's write count and returns the new count.
	AdvanceSeq(ctx context.Context, n int64) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// row is one stored row: a leaf (Value set) or an object's priority (Value nil).
type row struct {
	Path     string
	Value    json.RawMessage
	Priority json.RawMessage
}

// rowsOf returns the rows that store v at p.
func rowsOf(p Path, v *Node) []row {
	var rows []row
	_ = v.leaves(p, func(at Path, n *Node, leaf bool) error {
		r := row{Path: at.String()}
		if leaf {
			r.Value = storedLeaf(n)
		}
		if n.priority != nil {
			r.Priority = storedLeaf(n.priority)
		}
		rows = append(rows, r)
		return nil
	})
	return rows
}

// storedLeaf encodes a leaf for a jsonb column. Postgres cannot hold U+0000 in jsonb text,
// while Firebase accepts it in values (the emulator 4.11.2 stores and returns "a\u0000b"), so
// a string holding one is stored as a one-element array of its base64 UTF-8 bytes. Leaves
// are never arrays otherwise, so the form is unambiguous.
func storedLeaf(n *Node) json.RawMessage {
	if n.kind == KindString && strings.IndexByte(n.s, 0) >= 0 {
		return json.RawMessage(`["` + base64.StdEncoding.EncodeToString([]byte(n.s)) + `"]`)
	}
	if n.kind == KindNumber && !n.isInt {
		return appendStoredDouble(nil, n.n)
	}
	return n.appendLeaf(nil)
}

// treeOf builds the value at base from its rows: base's own row and those below it. It
// builds in place, since Node.Set copies on every call. The leaves of one object usually
// arrive one after another, so the last parent is kept rather than found again from the root.
func treeOf(base Path, rows []row) (*Node, error) {
	prefix := base.String()
	var root *Node
	var lastParent string // the path, relative to base, of lastObj
	var lastObj *Node     // nil when no parent is kept
	pruning := false
	for _, r := range rows {
		rel := strings.TrimPrefix(strings.TrimPrefix(r.Path, prefix), "/")
		var prio *Node
		if r.Priority != nil {
			p, err := leafOf(r.Priority)
			if err != nil {
				return nil, err
			}
			prio = p
		}
		if r.Value == nil {
			// An object's priority: the object itself is built from its leaves, and is pruned
			// if none come.
			objectAt(&root, rel).priority = prio
			pruning = true
			continue
		}
		leaf, err := leafOf(r.Value)
		if err != nil {
			return nil, err
		}
		leaf.priority = prio
		if rel == "" {
			root, lastObj = leaf, nil
			continue
		}
		parent, key := "", rel
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			parent, key = rel[:i], rel[i+1:]
		}
		// The kept parent stays in the tree: only a leaf at base replaces an object that holds
		// it, and that forgets it. A leaf that replaces an object elsewhere has a parent of its
		// own, which it is kept as.
		if lastObj == nil || parent != lastParent {
			lastParent, lastObj = parent, objectAt(&root, parent)
		}
		lastObj.children[key] = leaf
	}
	if pruning {
		root = prune(root)
	}
	return root, nil
}

// objectAt returns the object at rel, a relative path, below *root, creating objects on the way
// and in place of leaves, and *root itself if needed. Used only while building a fresh tree.
func objectAt(root **Node, rel string) *Node {
	if *root == nil || (*root).kind != KindObject {
		*root = &Node{kind: KindObject, children: map[string]*Node{}}
	}
	n := *root
	for rel != "" {
		var k string
		k, rel, _ = strings.Cut(rel, "/")
		c := n.children[k]
		if c == nil || c.kind != KindObject {
			c = &Node{kind: KindObject, children: map[string]*Node{}}
			n.children[k] = c
		}
		n = c
	}
	return n
}

// prune drops objects left without children, which a priority row alone would make.
func prune(n *Node) *Node {
	if n == nil || n.kind != KindObject {
		return n
	}
	for k, c := range n.children {
		if c = prune(c); c == nil {
			delete(n.children, k)
		}
	}
	if len(n.children) == 0 {
		return nil
	}
	return n
}

// leafOf decodes a stored leaf value or priority (storedLeaf).
func leafOf(raw json.RawMessage) (*Node, error) {
	if n := plainLeaf(raw); n != nil {
		return n, nil
	}
	if len(raw) > 0 && raw[0] == '[' {
		var enc []string
		if err := json.Unmarshal(raw, &enc); err != nil || len(enc) != 1 {
			return nil, ErrInvalidData
		}
		b, err := base64.StdEncoding.DecodeString(enc[0])
		if err != nil {
			return nil, ErrInvalidData
		}
		return String(string(b)), nil
	}
	v, err := ParseData(raw, nil, len(raw))
	if err != nil {
		return nil, err
	}
	if !v.IsLeaf() {
		return nil, ErrInvalidData
	}
	return v, nil
}

// plainLeaf decodes, without a JSON decoder, the leaves that Postgres returns for nearly every
// row: true, false, a number, or a string with no escapes. It returns nil for anything else,
// which ParseData then reads, so both always agree.
func plainLeaf(raw []byte) *Node {
	if len(raw) == 0 {
		return nil
	}
	switch c := raw[0]; {
	case c == 't' && string(raw) == "true":
		return Bool(true)
	case c == 'f' && string(raw) == "false":
		return Bool(false)
	case c == '"':
		s := raw[1:]
		if len(s) == 0 || s[len(s)-1] != '"' {
			return nil
		}
		s = s[:len(s)-1]
		for _, b := range s {
			if b == '\\' || b == '"' || b < 0x20 {
				return nil
			}
		}
		if !utf8.Valid(s) {
			return nil
		}
		return String(string(s))
	case c == '-' || '0' <= c && c <= '9':
		if !jsonNumber(raw) {
			return nil
		}
		f, i, isInt, ok := parseNumber(string(raw))
		switch {
		case !ok:
			return nil
		case isInt:
			return Int(i)
		}
		return Number(f)
	}
	return nil
}

// jsonNumber reports whether b is exactly a JSON number: -?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?
func jsonNumber(b []byte) bool {
	digits := func(i int) int {
		for i < len(b) && '0' <= b[i] && b[i] <= '9' {
			i++
		}
		return i
	}
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && '1' <= b[i] && b[i] <= '9':
		i = digits(i)
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		j := digits(i + 1)
		if j == i+1 {
			return false
		}
		i = j
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		j := digits(i)
		if j == i {
			return false
		}
		i = j
	}
	return i == len(b)
}
