package core

import (
	"slices"
)

// onDisconnect holds the writes a session registered for its disconnection, as the SDKs keep
// theirs in a SparseSnapshotTree (core/SparseSnapshotTree.ts): a set at a path replaces what
// was registered at and below it, and folds into a set registered above it; a cancel removes
// what is registered at and below a path, splitting a set above it into its children. The
// server values in them are resolved when they run.
type onDisconnect struct {
	entries []disconnectEntry
}

type disconnectEntry struct {
	path  Path
	value *Node // nil deletes
}

// remember registers a set of v at p.
func (o *onDisconnect) remember(p Path, v *Node) {
	o.entries = slices.DeleteFunc(o.entries, func(e disconnectEntry) bool { return p.Contains(e.path) })
	for i, e := range o.entries {
		if e.path.Contains(p) {
			o.entries[i].value = e.value.Set(e.path.Rel(p), v)
			return
		}
	}
	o.entries = append(o.entries, disconnectEntry{path: p, value: v})
}

// forget cancels what is registered at p and below.
func (o *onDisconnect) forget(p Path) {
	o.entries = slices.DeleteFunc(o.entries, func(e disconnectEntry) bool { return p.Contains(e.path) })
	for i, e := range o.entries {
		if !e.path.Contains(p) {
			continue
		}
		// A set above p. A leaf has no child to forget; an object (or a deletion, an empty
		// object) splits into sets of its children and forgets the one at p.
		if e.value.IsLeaf() {
			return
		}
		o.entries = slices.Delete(o.entries, i, i+1)
		for _, k := range e.value.Keys() {
			o.entries = append(o.entries, disconnectEntry{path: e.path.Child(k), value: e.value.Child(k)})
		}
		if pr := e.value.Priority(); pr != nil {
			o.entries = append(o.entries, disconnectEntry{path: e.path.Child(PriorityKey), value: pr})
		}
		o.forget(p)
		return
	}
}

// write is the registered writes as one write: a set when there is one entry, else an update
// at the entries' deepest common path, which the emulator pushes to listeners as a single
// merge (B-emulator-oracle.md §1.6). nil when nothing is registered. Its Size is that of the
// data it writes, which counts against the database's write rate as any write does.
func (o *onDisconnect) write() *Write {
	var size int64
	for _, e := range o.entries {
		size += int64(len(e.value.ExportJSON()))
	}
	switch len(o.entries) {
	case 0:
		return nil
	case 1:
		e := o.entries[0]
		return &Write{Path: e.path, Value: e.value, Size: size}
	}
	base := o.entries[0].path
	for _, e := range o.entries[1:] {
		n := 0
		for n < len(base) && n < len(e.path) && base[n] == e.path[n] {
			n++
		}
		base = base[:n]
	}
	if n := len(base); n > 0 && slices.ContainsFunc(o.entries, func(e disconnectEntry) bool { return len(e.path) == n }) {
		// An entry at the common path itself cannot be one of the update's children.
		base = base[:n-1]
	}
	w := &Write{Path: base, Update: make([]Child, 0, len(o.entries)), Size: size}
	for _, e := range o.entries {
		w.Update = append(w.Update, Child{Path: base.Rel(e.path), Value: e.value})
	}
	slices.SortStableFunc(w.Update, func(a, b Child) int { return comparePaths(a.Path, b.Path) })
	return w
}
