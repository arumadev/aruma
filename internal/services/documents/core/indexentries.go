package core

import (
	"cmp"
	"slices"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// A document's index entries (ADR-025 §2, §3): the single-field indexes of each of its fields,
// as its field configuration has them, and the composite indexes of its collection group. A
// commit writes the difference between the entries of each document's image before and after
// (sequencer.go); a build writes a document's entries whole (indexbuild.go).

// overrideKey names a field configuration: a collection group and a canonical field path,
// index.AllFields for the group's default, index.DefaultCollectionGroup for the database's.
type overrideKey struct{ group, path string }

// fieldConfigs is a database's explicit single-field configurations.
type fieldConfigs map[overrideKey][]index.SingleFieldIndex

// automaticIndexes is what a field has when nothing overrides it: ascending, descending and
// array-contains at collection scope; collection-group scope is not maintained by default
// (ADR-025 §2; index/automatic.go).
var automaticIndexes = []index.SingleFieldIndex{
	{Scope: index.ScopeCollection, Mode: index.Ascending},
	{Scope: index.ScopeCollection, Mode: index.Descending},
	{Scope: index.ScopeCollection, Mode: index.Contains},
}

// groupDefault is what a top-level field of group inherits: the group's own default, the
// database's, or the automatic indexes.
func (c fieldConfigs) groupDefault(group string) []index.SingleFieldIndex {
	if ixs, ok := c[overrideKey{group, index.AllFields}]; ok {
		return ixs
	}
	if ixs, ok := c[overrideKey{index.DefaultCollectionGroup, index.AllFields}]; ok {
		return ixs
	}
	return automaticIndexes
}

// governing is the configuration of path: its own, else the one its parent's governs with
// (inherited), since an override applies to the subfields that have none of their own.
func (c fieldConfigs) governing(group, path string, inherited []index.SingleFieldIndex) []index.SingleFieldIndex {
	if ixs, ok := c[overrideKey{group, path}]; ok {
		return ixs
	}
	return inherited
}

// effective is the configuration that governs the canonical path in group.
func (c fieldConfigs) effective(group, path string) []index.SingleFieldIndex {
	if path == index.AllFields {
		if group == index.DefaultCollectionGroup {
			return c.governing(group, path, automaticIndexes)
		}
		return c.groupDefault(group)
	}
	fp, err := value.ParseFieldPath(path)
	if err != nil {
		return c.groupDefault(group)
	}
	ixs := c.groupDefault(group)
	for i := range fp {
		ixs = c.governing(group, fp[:i+1].String(), ixs)
	}
	return ixs
}

// Production's limits on a document's entries (ADR-025 §2; IX §1.5).
const (
	maxEntriesPerDocument = 40_000
	maxEntrySize          = 7_680 // 7.5 KiB
	maxEntriesSize        = 8 << 20
	// entryOverhead is what an entry costs besides its document's name and its values, as
	// the storage-size page counts it (provisional: the formula is docs-only).
	entryOverhead = 32
)

var (
	// ErrIndexEntryTooLarge is production's "Index entry is too large." (f0-errors.json,
	// name_bytes_6127, with google.rpc.ErrorInfo reason INDEX_ENTRY_TOO_LARGE, which the
	// transport adds: wire.IndexEntryTooLarge). Recorded for a name of 6,144 bytes as
	// value.NameSize counts it, with or without fields; an entry over 7.5 KiB gets it too
	// (provisional).
	ErrIndexEntryTooLarge = &Error{InvalidArgument, "Index entry is too large."}
	// ErrTooManyIndexEntries is a document with more than 40,000 index entries, or 8 MiB of
	// them. Provisional text (Datastore's), not recorded.
	ErrTooManyIndexEntries = &Error{InvalidArgument, "too many index entries for entity"}
)

// maxIndexedName is the name size whose key-index entry production refuses (name_bytes_6127:
// 6,144 by value.NameSize, the most a name may have).
const maxIndexedName = 6_144

// entry is one index entry: its index and its whole key.
type entry struct {
	index int64
	key   string
}

// entrySet is a document's entries.
type entrySet map[entry]struct{}

// docEntries builds a document's entries.
type docEntries struct {
	path     Path
	group    string
	coll     []byte // the COLLECTION-scope prefix, after the version byte
	nameAsc  []byte // the name part and its length
	nameDesc []byte
	nameSize int
	set      entrySet
	// size is the entries' total size; tooLarge records an entry over maxEntrySize.
	size     int
	tooLarge bool
}

func newDocEntries(p Path) *docEntries {
	d := &docEntries{path: p, group: p.CollectionID(), set: entrySet{}, nameSize: value.NameSize(p.String())}
	d.coll = appendCollectionPart(nil, p.Parent(), d.group)
	d.nameAsc = AppendIndexName(nil, p, false)
	d.nameDesc = AppendIndexName(nil, p, true)
	return d
}

// add records one entry of index id: its values' parts in parts, of the sizes given.
func (d *docEntries) add(id int64, scope index.Scope, nameDesc bool, valueSize int, parts ...[]byte) {
	n := 1 + len(d.nameAsc)
	if scope == index.ScopeCollection {
		n += len(d.coll)
	}
	for _, p := range parts {
		n += len(p)
	}
	k := make([]byte, 0, n)
	k = append(k, value.KeyVersion)
	if scope == index.ScopeCollection {
		k = append(k, d.coll...)
	}
	for _, p := range parts {
		k = append(k, p...)
	}
	if nameDesc {
		k = append(k, d.nameDesc...)
	} else {
		k = append(k, d.nameAsc...)
	}
	e := entry{id, string(k)}
	if _, dup := d.set[e]; dup {
		return
	}
	d.set[e] = struct{}{}
	size := d.nameSize + valueSize + entryOverhead
	d.size += size
	if size > maxEntrySize {
		d.tooLarge = true
	}
}

// indexedSize is what an entry counts of a value: the index keeps 1500 bytes of it.
func indexedSize(v value.Value) int { return min(value.IndexSize(v), value.IndexedLimit) }

// valuePart returns a value's part of a key.
func valuePart(v value.Value, desc bool) []byte { return AppendIndexValue(nil, v, desc) }

// distinctElements returns an array's elements once per key: the entries of array-contains.
func distinctElements(v value.Value) []value.Value {
	els := v.Elements()
	seen := make(map[string]bool, len(els))
	out := make([]value.Value, 0, len(els))
	for _, e := range els {
		k := string(value.Key(e))
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// singleField adds the entries of the single-field indexes the configuration c gives each field
// of fields, a map at prefix (canonical, "" for the document), whose own fields inherit
// inherited.
func (d *docEntries) singleField(c fieldConfigs, fields value.Value, prefix string, inherited []index.SingleFieldIndex) {
	for _, f := range fields.Fields() {
		path := value.FieldPath{f.Name}.String()
		if prefix != "" {
			path = prefix + "." + path
		}
		ixs := c.governing(d.group, path, inherited)
		v := f.Value
		var asc, desc []byte
		for _, ix := range ixs {
			id := SingleFieldIndexID(d.group, ix.Scope, path, ix.Mode)
			switch ix.Mode {
			case index.Ascending:
				if asc == nil {
					asc = valuePart(v, false)
				}
				d.add(id, ix.Scope, false, indexedSize(v), asc)
			case index.Descending:
				if desc == nil {
					desc = valuePart(v, true)
				}
				d.add(id, ix.Scope, true, indexedSize(v), desc)
			case index.Contains:
				if v.Kind() != value.KindArray {
					continue
				}
				for _, e := range distinctElements(v) {
					d.add(id, ix.Scope, false, indexedSize(e), valuePart(e, false))
				}
			}
		}
		if value.ClassOf(v) == value.ClassMap {
			d.singleField(c, v, path, ixs)
		}
	}
}

// composite adds the entries of composite index ix: one per element of its array-contains
// field, none when a field is missing (or the array-contains one is no array or empty).
func (d *docEntries) composite(ix *composite, fields value.Value) {
	n := len(ix.fields) - 1 // the last is __name__
	parts := make([][]byte, n)
	size := 0
	contains := -1
	var elements []value.Value
	for i, f := range ix.fields[:n] {
		v, ok := fields.Lookup(f.path)
		if !ok {
			return
		}
		switch f.mode {
		case index.Contains:
			if v.Kind() != value.KindArray {
				return
			}
			if elements = distinctElements(v); len(elements) == 0 {
				return
			}
			contains = i
		default:
			parts[i] = valuePart(v, f.mode == index.Descending)
			size += indexedSize(v)
		}
	}
	nameDesc := ix.fields[n].mode == index.Descending
	if contains < 0 {
		d.add(ix.id, ix.scope, nameDesc, size, parts...)
		return
	}
	for _, e := range elements {
		parts[contains] = valuePart(e, false)
		d.add(ix.id, ix.scope, nameDesc, size+indexedSize(e), parts...)
	}
}

// entriesOf returns a document's entries under the catalog: the single-field ones of the
// configuration c, and with composites, those of the group's composite indexes.
func entriesOf(cat *catalog, c fieldConfigs, p Path, fields value.Value, composites bool) *docEntries {
	d := newDocEntries(p)
	d.singleField(c, fields, "", c.groupDefault(d.group))
	if composites {
		for _, ix := range cat.byGroup(d.group) {
			d.composite(ix, fields)
		}
	}
	return d
}

// check holds a document's entries to production's limits.
func (d *docEntries) check() *Error {
	switch {
	case d.nameSize >= maxIndexedName || d.tooLarge:
		return ErrIndexEntryTooLarge
	case len(d.set) > maxEntriesPerDocument || d.size > maxEntriesSize:
		return ErrTooManyIndexEntries
	}
	return nil
}

// indexChanges is a commit's index statements' arrays (queries.DeleteIndexEntriesParams,
// queries.InsertIndexEntriesParams).
type indexChanges struct {
	delIDs               []int64
	delKeys              [][]byte
	insIDs               []int64
	insKeys, insFullKeys [][]byte
}

// indexArgs computes the index entries the changed documents of the plan delete and insert under
// cat, and holds every document the commit leaves to production's limits. The entries before
// are those of the current configuration and, while a field configuration is built, of the one
// before it, whose entries a document not yet rebuilt still has: a commit leaves a document with
// exactly the current configuration's entries (indexbuild.go).
func (p *plan) indexArgs(cat *catalog) (indexChanges, error) {
	var out indexChanges
	var dels, ins []entry
	for _, d := range p.docs {
		if !d.changed() {
			continue
		}
		var after *docEntries
		if d.exists {
			after = entriesOf(cat, cat.fields, d.path, d.fields, true)
			if err := after.check(); err != nil {
				return indexChanges{}, err
			}
		}
		var before, prior entrySet
		if d.before != nil {
			before = entriesOf(cat, cat.fields, d.path, d.before.fields, true).set
			if cat.prior != nil {
				prior = entriesOf(cat, cat.prior, d.path, d.before.fields, false).set
			}
		}
		if after != nil {
			for e := range after.set {
				if _, ok := before[e]; !ok {
					ins = append(ins, e)
				}
			}
		}
		for _, set := range []entrySet{before, prior} {
			for e := range set {
				if _, ok := after.entries()[e]; !ok {
					dels = append(dels, e)
				}
			}
		}
	}
	dels = sortedEntries(dels)
	for _, e := range dels {
		out.delIDs = append(out.delIDs, e.index)
		out.delKeys = append(out.delKeys, storedKey([]byte(e.key)))
	}
	for _, e := range sortedEntries(ins) {
		key, full := storedEntry([]byte(e.key))
		out.insIDs, out.insKeys, out.insFullKeys = append(out.insIDs, e.index), append(out.insKeys, key), append(out.insFullKeys, full)
	}
	return out, nil
}

// entries returns the set, empty for no document.
func (d *docEntries) entries() entrySet {
	if d == nil {
		return nil
	}
	return d.set
}

// sortedEntries orders entries by index and key, once each, so that concurrent commits touch
// index_entries rows in one order.
func sortedEntries(es []entry) []entry {
	slices.SortFunc(es, func(a, b entry) int { return cmp.Or(cmp.Compare(a.index, b.index), strings.Compare(a.key, b.key)) })
	return slices.CompactFunc(es, func(a, b entry) bool { return a == b })
}

// storedEntry returns an entry's key as stored and its whole key when it is cut.
func storedEntry(k []byte) (key, full []byte) {
	if len(k) > cutLen {
		return storedKey(k), k
	}
	return k, nil
}
