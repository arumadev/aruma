package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
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
	return n.appendLeaf(nil)
}

// treeOf builds the value at base from its rows: base's own row and those below it. It
// builds in place, since Node.Set copies on every call.
func treeOf(base Path, rows []row) (*Node, error) {
	prefix := base.String()
	var root *Node
	for _, r := range rows {
		rel := strings.TrimPrefix(strings.TrimPrefix(r.Path, prefix), "/")
		var keys Path
		if rel != "" {
			keys = strings.Split(rel, "/")
		}
		var prio *Node
		if r.Priority != nil {
			p, err := leafOf(r.Priority)
			if err != nil {
				return nil, err
			}
			prio = p
		}
		if r.Value == nil {
			// An object's priority: the object itself is built from its leaves.
			root = ensureObject(root, keys)
			root.objectAt(keys).priority = prio
			continue
		}
		leaf, err := leafOf(r.Value)
		if err != nil {
			return nil, err
		}
		leaf.priority = prio
		if len(keys) == 0 {
			root = leaf
			continue
		}
		root = ensureObject(root, keys[:len(keys)-1])
		root.objectAt(keys[:len(keys)-1]).children[keys[len(keys)-1]] = leaf
	}
	return prune(root), nil
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

// ensureObject makes sure an object exists at keys below root, creating objects on the way,
// and returns the (possibly new) root. Used only while building a fresh tree.
func ensureObject(root *Node, keys Path) *Node {
	if root == nil || root.kind != KindObject {
		root = &Node{kind: KindObject, children: map[string]*Node{}}
	}
	n := root
	for _, k := range keys {
		c := n.children[k]
		if c == nil || c.kind != KindObject {
			c = &Node{kind: KindObject, children: map[string]*Node{}}
			n.children[k] = c
		}
		n = c
	}
	return root
}

func (n *Node) objectAt(keys Path) *Node {
	for _, k := range keys {
		n = n.children[k]
	}
	return n
}

// leafOf decodes a stored leaf value or priority (storedLeaf).
func leafOf(raw json.RawMessage) (*Node, error) {
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
