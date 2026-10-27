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

// Production's limits on a document's index entries and on a commit's, as f3-index-entries.json
// recorded them (ADR-025 §2). The sizes are fitted to the recorded thresholds, for a document
// named with L bytes below documents/ in five ids under collections "c": its name's size D
// (value.NameSize) and its parent document's P.
//
//   - Every document has an entry of its name: D + P + 62, at most maxEntrySize. The name alone
//     is refused at L 4,497 and accepted at 4,496.
//   - A field's entry, single-field or composite, holds the name twice and the parent's:
//     2·D + P, each indexed field's name (its string size) and value, and 79. One integer field
//     is refused at L 2,878 and accepted at 2,877; a 1,500-byte field name with an integer, or
//     one 1,500- or 3,000-byte string, at 2,342 and 2,341. So a field's name counts byte for
//     byte, a string counts its first 1,500 bytes (value.IndexedLimit), and against it the
//     integer 1 counts 1: its storage size, 8, would refuse the 2,341-byte name with the long
//     field name. An integer counts 1; every other value its indexed storage size.
//   - At most 40,000 entries, an array-contains entry counting two: the recorded 20,000
//     integer fields, 19,999 subfields of a map, 19,999 distinct elements of an array. The
//     name's entry does not count.
//   - A document's entry changes in a commit, those it inserts and twice those it deletes, at
//     most maxTransactionSize: a new document of L 2,677 with 5,155 integer fields (10,310
//     entries of 7,637 bytes) is accepted, one with 5,156 refused "Transaction too big.
//     Decrease transaction size.", and so is the 5,155-field document's delete. Every recorded
//     case is one document; production's bound on a commit of many is not recorded, and the
//     sum of its documents' changes is not held to it, so that a commit production may accept
//     is not refused (a 500-write batch deleting documents of 200 fields sums about 200 MB).
//     A commit whose sum passes it is counted (PGStore.LargeCommits) and logged.
//
// Besides, a name whose size reaches 6,144 has an entry too large whatever its depth
// (f0-errors.json name_bytes_6127).
const (
	maxEntriesPerDocument = 40_000
	maxEntrySize          = 8_192
	nameEntryOverhead     = 62
	fieldEntryOverhead    = 79
	// maxTransactionSize is fitted between the recorded totals, 78,737,470 and 78,752,744.
	maxTransactionSize = 78_750_000
)

var (
	// ErrIndexEntryTooLarge is production's "Index entry is too large." (f0-errors.json,
	// name_bytes_6127; f3-index-entries.json entry_*), with google.rpc.ErrorInfo reason
	// INDEX_ENTRY_TOO_LARGE, which the transport adds: wire.IndexEntryTooLarge.
	ErrIndexEntryTooLarge = &Error{InvalidArgument, "Index entry is too large."}
	// ErrTransactionTooBig is a commit whose entry changes of one document pass
	// maxTransactionSize (f3-index-entries.json total.*).
	ErrTransactionTooBig = &Error{InvalidArgument, "Transaction too big. Decrease transaction size."}
)

// TooManyIndexEntriesError is a document with more than 40,000 index entries: "too many index
// entries for entity /aruma-ref-f3-ix/fields-20001" (f3-index-entries.json), the document's
// path after a "/", with the google.rpc.ErrorInfo the transport adds
// (wire.TooManyIndexEntries).
type TooManyIndexEntriesError struct{ Path Path }

// DocPath is the document as the error names it.
func (e *TooManyIndexEntriesError) DocPath() string { return "/" + e.Path.String() }

func (e *TooManyIndexEntriesError) Error() string {
	return "documents: too many index entries for entity " + e.DocPath()
}

// maxIndexedName is the name size whose entry production refuses whatever the document's depth
// (name_bytes_6127: 6,144 by value.NameSize, the most a name may have).
const maxIndexedName = 6_144

// entry is one index entry: its index and its whole key.
type entry struct {
	index int64
	key   string
}

// entrySet is a document's entries, each with its size.
type entrySet map[entry]int

// docEntries builds a document's entries.
type docEntries struct {
	path     Path
	group    string
	coll     []byte // the COLLECTION-scope prefix, after the version byte
	nameAsc  []byte // the name part and its length
	nameDesc []byte
	// nameSize and parentSize are the sizes of the document's name and of its parent's.
	nameSize, parentSize int
	set                  entrySet
	// count is the entries as maxEntriesPerDocument counts them; tooLarge records an entry
	// over maxEntrySize.
	count    int
	tooLarge bool
}

func newDocEntries(p Path) *docEntries {
	d := &docEntries{path: p, group: p.CollectionID(), set: entrySet{}, nameSize: value.NameSize(p.String())}
	d.parentSize = d.nameSize - len(p.CollectionID()) - len(p.ID()) - 2
	d.coll = appendCollectionPart(nil, p.Parent(), d.group)
	d.nameAsc = AppendIndexName(nil, p, false)
	d.nameDesc = AppendIndexName(nil, p, true)
	return d
}

// add records one entry of index id: its values' parts in parts, the indexed fields costing
// fieldsSize (entryFieldSize). cost is what it counts against maxEntriesPerDocument.
func (d *docEntries) add(id int64, scope index.Scope, nameDesc bool, fieldsSize, cost int, parts ...[]byte) {
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
	size := 2*d.nameSize + d.parentSize + fieldsSize + fieldEntryOverhead
	d.set[e] = size
	d.count += cost
	if size > maxEntrySize {
		d.tooLarge = true
	}
}

// entryFieldSize is what an entry counts of one indexed field: its name's string size and its value.
func entryFieldSize(path string, v value.Value) int { return len(path) + 1 + indexedSize(v) }

// indexedSize is what an entry counts of a value: the index keeps 1500 bytes of it, and an
// integer counts 1 (see maxEntrySize).
func indexedSize(v value.Value) int {
	if v.Kind() == value.KindInteger {
		return 1
	}
	return min(value.IndexSize(v), value.IndexedLimit)
}

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
				d.add(id, ix.Scope, false, entryFieldSize(path, v), 1, asc)
			case index.Descending:
				if desc == nil {
					desc = valuePart(v, true)
				}
				d.add(id, ix.Scope, true, entryFieldSize(path, v), 1, desc)
			case index.Contains:
				if v.Kind() != value.KindArray {
					continue
				}
				for _, e := range distinctElements(v) {
					// An element counts two entries (f3-index-entries.json array.*).
					d.add(id, ix.Scope, false, entryFieldSize(path, e), 2, valuePart(e, false))
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
			size += entryFieldSize(f.canon, v)
		}
	}
	nameDesc := ix.fields[n].mode == index.Descending
	if contains < 0 {
		d.add(ix.id, ix.scope, nameDesc, size, 1, parts...)
		return
	}
	for _, e := range elements {
		parts[contains] = valuePart(e, false)
		d.add(ix.id, ix.scope, nameDesc, size+entryFieldSize(ix.fields[contains].canon, e), 1, parts...)
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
func (d *docEntries) check() error {
	switch {
	case d.nameSize >= maxIndexedName || d.nameSize+d.parentSize+nameEntryOverhead > maxEntrySize || d.tooLarge:
		return ErrIndexEntryTooLarge
	case d.count > maxEntriesPerDocument:
		return &TooManyIndexEntriesError{Path: d.path}
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
	// size is the commit's entry changes as maxTransactionSize counts a document's, summed.
	size int
}

// indexArgs computes the index entries the changed documents of the plan delete and insert under
// cat, and holds every document the commit leaves to production's limits. The entries before
// are those of the current configuration and, while a field configuration is built, of the one
// before it, whose entries a document not yet rebuilt still has: a commit leaves a document with
// exactly the current configuration's entries (indexbuild.go). Each document's entry changes
// are held to maxTransactionSize: those it inserts, and twice those it deletes.
func (p *plan) indexArgs(cat *catalog) (indexChanges, error) {
	var out indexChanges
	var dels, ins []entry
	for _, d := range p.docs {
		if !d.changed() {
			continue
		}
		size := 0
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
			for e, n := range after.set {
				if _, ok := before[e]; !ok {
					ins = append(ins, e)
					size += n
				}
			}
		}
		for i, set := range []entrySet{before, prior} {
			for e, n := range set {
				if _, ok := after.entries()[e]; !ok {
					dels = append(dels, e)
					if _, both := before[e]; i == 0 || !both {
						size += 2 * n
					}
				}
			}
		}
		if size > maxTransactionSize {
			return indexChanges{}, ErrTransactionTooBig
		}
		out.size += size
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
