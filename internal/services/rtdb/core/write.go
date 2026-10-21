package core

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
)

// Write is one write command: a set of Value at Path, or, when Update is not nil, an update
// that sets each child path below Path at once (a multi-path update is atomic).
type Write struct {
	Path   Path
	Value  *Node
	Update []Child
	// Hash makes a set conditional: it applies only while the hash of the value at Path is
	// still Hash, else ErrDataStale. Transactions use it.
	Hash *string
	// Size is the write's size as received, counted against the per-minute write limit.
	Size int64
	// Separately makes rules check each child of an update as a write of its own against
	// the data before it, dropping the children they deny, as onDisconnect writes run when
	// the session closes (emulator 4.11.2, golden ondisconnect_rules.json).
	Separately bool
}

// Child is one path of an update, relative to the update's path.
type Child struct {
	Path  Path
	Value *Node
}

// IsUpdate reports whether w is a multi-path update.
func (w *Write) IsUpdate() bool { return w.Update != nil }

// ParseSet reads a set as the SDKs send it (p, o): export-format data written at p, which
// ParseWritePath read, and an optional hash. Callers check permission on p first, so data
// from a client who may not write is never parsed.
func ParseSet(p Path, data json.RawMessage, hash *string, maxString int) (*Write, error) {
	v, err := parseAt(p, data, maxString)
	if err != nil {
		return nil, err
	}
	return &Write{Path: p, Value: v, Hash: hash, Size: int64(len(data))}, nil
}

// parseAt parses data written at p. A priority path takes a priority, not a value.
func parseAt(p Path, data json.RawMessage, maxString int) (*Node, error) {
	if !p.IsPriority() {
		return ParseData(data, p, maxString)
	}
	v, err := ParseData(data, p.Parent(), maxString)
	if err != nil {
		return nil, err
	}
	if v != nil && (!v.IsLeaf() || v.Kind() == KindBool || v.priority != nil) {
		return nil, ErrInvalidPriority
	}
	return v, nil
}

// ParseUpdate reads an update as the SDKs send it (m, om): an object whose keys are paths
// relative to base. No key may be another's ancestor, as validateFirebaseMergePaths requires.
// The children are put in key order, the order the SDKs' own update applies them in.
func ParseUpdate(base Path, data json.RawMessage, maxString int) (*Write, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrInvalidData
	}
	w := &Write{Path: base, Update: []Child{}, Size: int64(len(data))}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, ErrInvalidData
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, ErrInvalidData
		}
		rel, err := ParseWritePath(tok.(string))
		if err != nil {
			return nil, err
		}
		if rel.IsRoot() {
			return nil, ErrInvalidPath
		}
		full := base.Join(rel)
		if err := full.checkSize(); err != nil {
			return nil, err
		}
		v, err := parseAt(full, raw, maxString)
		if err != nil {
			return nil, err
		}
		w.Update = append(w.Update, Child{Path: rel, Value: v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, ErrInvalidData
	}
	slices.SortStableFunc(w.Update, func(a, b Child) int { return comparePaths(a.Path, b.Path) })
	for i := 1; i < len(w.Update); i++ {
		// Sorted, a path comes right before the paths below it.
		if w.Update[i-1].Path.Contains(w.Update[i].Path) {
			return nil, ErrOverlappingPaths
		}
	}
	return w, nil
}

// comparePaths orders paths key by key, a path before those below it.
func comparePaths(a, b Path) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := CompareKeys(a[i], b[i]); c != 0 {
			return c
		}
	}
	return len(a) - len(b)
}

// Change is a write as applied, server values resolved, as listeners receive it: exactly
// what was written, not a diff (B-emulator-oracle.md §2).
type Change struct {
	Path  Path
	Value *Node   // a set
	Merge []Child // an update; nil for a set
}

// resolve prepares w in tx at server time now, in milliseconds, writing nothing: a
// conditional write whose hash no longer matches returns ErrDataStale, and the server values
// are filled in, every child of an update against the state before it. The rules check the
// change resolve returns, as Firebase's see resolved values, before store writes it.
func (w *Write) resolve(ctx context.Context, tx StoreTx, now int64) (Change, error) {
	if w.Hash != nil {
		cur, err := tx.Read(ctx, w.Path)
		if err != nil {
			return Change{}, err
		}
		if Hash(cur) != *w.Hash {
			return Change{}, ErrDataStale
		}
	}
	if !w.IsUpdate() {
		v, err := resolveAt(ctx, tx, w.Path, w.Value, now)
		if err != nil {
			return Change{}, err
		}
		return Change{Path: w.Path, Value: v}, nil
	}
	merge := make([]Child, len(w.Update))
	for i, c := range w.Update {
		v, err := resolveAt(ctx, tx, w.Path.Join(c.Path), c.Value, now)
		if err != nil {
			return Change{}, err
		}
		merge[i] = Child{Path: c.Path, Value: v}
	}
	return Change{Path: w.Path, Merge: merge}, nil
}

// parts splits a change into the writes rules check one by one when they check an update's
// children separately: a set is one part, an update one per child.
func (ch Change) parts() []Change {
	if ch.Merge == nil {
		return []Change{ch}
	}
	out := make([]Change, len(ch.Merge))
	for i, c := range ch.Merge {
		out[i] = Change{Path: ch.Path, Merge: []Child{c}}
	}
	return out
}

// store writes a resolved change in tx.
func (ch Change) store(ctx context.Context, tx StoreTx) error {
	if ch.Merge == nil {
		return tx.Write(ctx, ch.Path, ch.Value)
	}
	for _, c := range ch.Merge {
		if err := tx.Write(ctx, ch.Path.Join(c.Path), c.Value); err != nil {
			return err
		}
	}
	return nil
}

// resolveAt fills in the server values of v, written at p, reading only the locations its
// increments need.
func resolveAt(ctx context.Context, tx StoreTx, p Path, v *Node, now int64) (*Node, error) {
	if !v.HasServerValues() {
		return v, nil
	}
	existing := map[string]*Node{}
	for _, rel := range v.IncrementPaths() {
		cur, err := tx.Read(ctx, p.Join(rel))
		if err != nil {
			return nil, err
		}
		existing[rel.String()] = cur
	}
	return ResolveServerValues(v, now, existing), nil
}
