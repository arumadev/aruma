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
}

// Child is one path of an update, relative to the update's path.
type Child struct {
	Path  Path
	Value *Node
}

// IsUpdate reports whether w is a multi-path update.
func (w *Write) IsUpdate() bool { return w.Update != nil }

// ParseSet reads a set as the SDKs send it (p, o): path, export-format data, optional hash.
func ParseSet(path string, data json.RawMessage, hash *string, maxString int) (*Write, error) {
	p, err := ParseWritePath(path)
	if err != nil {
		return nil, err
	}
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
// relative to path. No key may be another's ancestor, as validateFirebaseMergePaths requires.
func ParseUpdate(path string, data json.RawMessage, maxString int) (*Write, error) {
	base, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
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
	if overlapping(w.Update) {
		return nil, ErrOverlappingPaths
	}
	return w, nil
}

func overlapping(children []Child) bool {
	paths := make([]Path, len(children))
	for i, c := range children {
		paths[i] = c.Path
	}
	slices.SortFunc(paths, func(a, b Path) int { return slices.Compare(a, b) })
	for i := 1; i < len(paths); i++ {
		if paths[i-1].Contains(paths[i]) {
			return true
		}
	}
	return false
}

// Change is a write as applied, server values resolved, as listeners receive it: exactly
// what was written, not a diff (B-emulator-oracle.md §2).
type Change struct {
	Path  Path
	Value *Node   // a set
	Merge []Child // an update; nil for a set
}

// apply runs w in tx at server time now, in milliseconds. A conditional write whose hash no
// longer matches returns ErrDataStale and writes nothing. Validation is complete before the
// first row changes, so an error other than a storage error leaves tx untouched.
func (w *Write) apply(ctx context.Context, tx StoreTx, now int64) (Change, error) {
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
		if err := tx.Write(ctx, w.Path, v); err != nil {
			return Change{}, err
		}
		return Change{Path: w.Path, Value: v}, nil
	}
	// Resolve every child against the state before the update, then write them all.
	merge := make([]Child, len(w.Update))
	for i, c := range w.Update {
		v, err := resolveAt(ctx, tx, w.Path.Join(c.Path), c.Value, now)
		if err != nil {
			return Change{}, err
		}
		merge[i] = Child{Path: c.Path, Value: v}
	}
	for _, c := range merge {
		if err := tx.Write(ctx, w.Path.Join(c.Path), c.Value); err != nil {
			return Change{}, err
		}
	}
	return Change{Path: w.Path, Merge: merge}, nil
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
