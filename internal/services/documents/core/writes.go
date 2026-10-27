package core

import (
	"bytes"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// lockedDoc is a document as a commit's first batch read it under its row lock.
type lockedDoc struct {
	seq        int64
	fields     value.Value
	createTime time.Time
	updateTime time.Time
	// stored is its documents.doc as read, the fields alone (docpb.go); nil when not read.
	stored []byte
}

// docChange is one document's part of a commit: its writes folded in order, from the document
// as locked (before) to the document as the commit leaves it.
type docChange struct {
	path   Path
	keys   docKeys
	writes int // how many of the commit's writes name it
	// mask is the update mask of a masked write (changelog.update_mask when it is the only one).
	masked bool
	mask   []value.FieldPath

	before *lockedDoc // nil: it did not exist
	exists bool
	fields value.Value
	// serverTime: a write set a field of it to the commit's time (TransformServerTime).
	serverTime bool
	// cur builds fields during apply; applied counts the writes applied to it.
	cur     docFields
	applied int
}

// changed reports whether the commit changes the document: a write that leaves it as it was
// changes nothing, keeps its update_time and is not in the changelog (WriteResult.update_time in
// write.proto; f1-write-results.json, rest.update_unchanged). A document a write gave the
// commit's time changes: its fields are known only once the commit is stamped, and the time is
// later than any it held, but for a field set within the same millisecond.
func (d *docChange) changed() bool {
	if d.before == nil || !d.exists {
		return (d.before != nil) != d.exists
	}
	return d.serverTime || !value.Identical(d.before.fields, d.fields)
}

// plan is a commit's writes grouped by document, in the order their rows are locked. F3 adds the
// index entries here; the commit path does not change shape.
type plan struct {
	writes []Write
	docs   []*docChange // by stored name key
	of     []*docChange // per write
	// serverTime: a write has a REQUEST_TIME transform, so the documents are built only once
	// the commit is stamped (sequencer.attempt).
	serverTime bool
	// transformed holds each write's transform results, as the last apply made them.
	transformed [][]value.Value
	// verify, when set, checks the documents as the first batch locked them (by stored key)
	// before the writes apply: what a transaction read (ADR-024 §6.4).
	verify func(locked map[string]*lockedDoc) error
}

func newPlan(writes []Write) *plan {
	p := &plan{writes: writes, of: make([]*docChange, len(writes)), transformed: make([][]value.Value, len(writes))}
	byKey := map[string]*docChange{}
	for i, w := range writes {
		k := nameKey(w.Path)
		d := byKey[string(k)]
		if d == nil {
			d = &docChange{path: w.Path, keys: keysOf(w.Path)}
			byKey[string(k)] = d
			p.docs = append(p.docs, d)
		}
		d.writes++
		if w.Kind == WriteUpdate && w.Masked {
			// A transformed field changes as a masked one does.
			d.masked, d.mask = true, slices.Clip(w.Mask)
			for _, t := range w.Transforms {
				d.mask = append(d.mask, t.Path)
			}
		}
		for _, t := range w.Transforms {
			if t.Kind == TransformServerTime {
				p.serverTime = true
			}
		}
		p.of[i] = d
	}
	slices.SortFunc(p.docs, func(a, b *docChange) int { return bytes.Compare(a.keys.stored, b.keys.stored) })
	return p
}

// storedKeys returns the documents' name_key values, in lock order.
func (p *plan) storedKeys() [][]byte {
	ks := make([][]byte, len(p.docs))
	for i, d := range p.docs {
		ks[i] = d.keys.stored
	}
	return ks
}

// MaxCommitWork bounds what a commit's writes rebuild of documents an earlier write of the
// commit already wrote, by weight (F2 re-review N1): a masked write rebuilds the top-level
// fields it changes, so many writes of one large field cost writes × its size. A document's
// first write is not counted, its cost being that of the document read, nor is a write that
// sets a whole document, which costs its own size. 2^22 costs at most about a second of one
// core across shapes, and a REQUEST_TIME commit applies its writes twice (sequencer.attempt).
// Recorded commits write a document at most twice, with small fields (parity
// request-work-bounds).
const MaxCommitWork = 1 << 22

// errCommitWork is a commit over MaxCommitWork. Our text.
var errCommitWork = &Error{InvalidArgument, "The commit's writes rebuild more than 4194304 values of documents they already wrote. Please split it into smaller commits."}

// apply folds the writes over the documents as locked (by stored key; absent if not there): each
// write's precondition is checked against the document as the writes before it left it, and the
// first that fails fails the commit with production's error. A write's transforms apply after
// its fields, in order, REQUEST_TIME as RequestTime(commitTime). It may run again with another
// commitTime: it starts from the documents as locked each time, and only REQUEST_TIME's values
// differ, which change neither a document's validity nor its size.
func (p *plan) apply(db DB, locked map[string]*lockedDoc, commitTime time.Time) error {
	for _, d := range p.docs {
		d.before = locked[string(d.keys.stored)]
		d.exists = d.before != nil
		d.serverTime = false
		d.applied = 0
		d.cur.reset(value.Map())
		if d.exists {
			d.cur.reset(d.before.fields)
		}
	}
	now := RequestTime(commitTime)
	// A failed precondition fails the commit; when several fail, production names a missing
	// document's first (f2-commit.json pre.two_failures, pre.two_failures_reversed), so the
	// writes after a failed one are still checked, but not applied.
	var failed *Error
	work := 0
	for i, w := range p.writes {
		// The transports refuse such names; this keeps one from ever being stored, since a name
		// key's segments are only read back right when they are UTF-8 (0xFF 0x01 would end one).
		if slices.ContainsFunc(w.Path, func(id string) bool { return !utf8.ValidString(id) }) {
			return &Error{InvalidArgument, "Request contains an invalid argument."}
		}
		d := p.of[i]
		if err := d.check(db, w.Precondition); err != nil {
			if failed == nil || failed.Code != NotFound && err.Code == NotFound {
				failed = err
			}
			continue
		}
		if failed != nil {
			continue
		}
		d.applied++
		switch w.Kind {
		case WriteUpdate:
			results, rebuilt, err := d.cur.update(db, w, now)
			if err != nil {
				return err
			}
			p.transformed[i] = results
			if slices.ContainsFunc(w.Transforms, func(t Transform) bool { return t.Kind == TransformServerTime }) {
				d.serverTime = true
			}
			if d.applied > 1 {
				if work += rebuilt; work > MaxCommitWork {
					return errCommitWork
				}
			}
			d.exists = true
		case WriteDelete:
			d.exists, d.serverTime = false, false
			d.cur.reset(value.Map())
		}
	}
	if failed != nil {
		return failed
	}
	for _, d := range p.docs {
		d.fields = d.cur.value()
	}
	return nil
}

// docFields is a document's fields as a commit's writes leave them, built a write at a time. A
// masked write changes the top-level fields its paths start with, and only those are rebuilt,
// checked and sized: base holds the fields as an earlier write left them and over those changed
// since, so many small writes of one large document cost their fields, not the document once
// per write (F2 re-review N1). value folds them together once.
type docFields struct {
	base  value.Value
	over  map[string]overField
	n     int // how many fields
	odd   int // how many named __x__ (oddName)
	size  int // value.DocumentSize, once sized
	sized bool
	valid bool // the fields passed value.Validate
}

// overField is a top-level field a write changed: its value (ok false: deleted) and its part
// of the document's size.
type overField struct {
	v    value.Value
	ok   bool
	size int
}

// reset makes fields the whole document, not yet sized nor checked.
func (f *docFields) reset(fields value.Value) {
	*f = docFields{base: fields, n: len(fields.Fields()), odd: oddFields(fields.Fields())}
}

// oddName reports whether a field is named __x__, as the keys of vectors and BSON shapes are.
func oddName(name string) bool {
	return len(name) >= 4 && strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")
}

func oddFields(fields []value.Field) int {
	n := 0
	for _, f := range fields {
		if oddName(f.Name) {
			n++
		}
	}
	return n
}

// get returns a field and its part of the document's size.
func (f *docFields) get(name string) (value.Value, bool, int) {
	if o, ok := f.over[name]; ok {
		return o.v, o.ok, o.size
	}
	v, ok := f.base.Get(name)
	if !ok {
		return value.Value{}, false, 0
	}
	return v, true, fieldSize(name, v)
}

// fieldsOf returns the fields of those names that the document holds, and the document's size
// without them.
func (f *docFields) fieldsOf(names []string) ([]value.Field, int) {
	fields := make([]value.Field, 0, len(names))
	size := f.size
	for _, n := range names {
		if v, ok, s := f.get(n); ok {
			fields = append(fields, value.Field{Name: n, Value: v})
			size -= s
		}
	}
	return fields, size
}

// fieldSize is a field's part of value.DocumentSize.
func fieldSize(name string, v value.Value) int { return len(name) + 1 + value.Size(v) }

// value returns the fields as a map.
func (f *docFields) value() value.Value {
	if len(f.over) == 0 {
		return f.base
	}
	fields := make([]value.Field, 0, f.n)
	for _, fl := range f.base.Fields() {
		if _, ok := f.over[fl.Name]; !ok {
			fields = append(fields, fl)
		}
	}
	for name, o := range f.over {
		if o.ok {
			fields = append(fields, value.Field{Name: name, Value: o.v})
		}
	}
	f.base, f.over = value.Map(fields...), nil
	return f.base
}

// update applies an update write and checks the document it leaves as production checks each
// write: valid, when a mask or a transform made it, then within the size limit. It returns the
// transforms' results and the weight of the fields it rebuilt in a document already there.
func (f *docFields) update(db DB, w Write, now value.Value) (results []value.Value, rebuilt int, err error) {
	name := w.Path.String()
	if !w.Masked {
		after := w.Fields
		if len(w.Transforms) > 0 {
			if after, results, err = applyTransforms(after, w.Transforms, now); err != nil {
				return nil, 0, err
			}
			if err := value.Validate(after); err != nil {
				return nil, 0, invalidDocument(err)
			}
		}
		size := value.DocumentSize(name, after)
		if size > value.MaxDocumentSize {
			return nil, 0, documentTooLarge(db.DocumentName(w.Path), size)
		}
		f.reset(after)
		f.size, f.sized, f.valid = size, true, len(w.Transforms) > 0
		return results, 0, nil
	}
	if !f.sized {
		f.size, f.sized = value.DocumentSize(name, f.value()), true
	}
	// The write reads and changes only the fields its paths start with (ApplyMask,
	// applyTransforms), so it applies to them alone.
	var names []string
	for _, p := range w.Mask {
		if len(p) > 0 && !p.IsDocumentID() {
			names = append(names, p[0])
		}
	}
	whole := false
	for _, t := range w.Transforms {
		if whole = whole || len(t.Path) == 0; !whole {
			names = append(names, t.Path[0])
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	before, size := f.fieldsOf(names)
	// ApplyMask and applyTransforms read a map that is a vector or a BSON shape as no map. Only a
	// map with a field named __x__ can be one, which Validate refuses a document; where one
	// could arise, in the document or in the part, at any step of the write, the write applies
	// to the whole document, as it does when a transform names no field.
	if whole || f.odd > 0 || slices.ContainsFunc(names, oddName) {
		whole = true
		for _, fl := range f.value().Fields() {
			names = append(names, fl.Name)
		}
		slices.Sort(names)
		names = slices.Compact(names)
		before, size = f.fieldsOf(names)
	}
	after := value.ApplyMask(value.Map(before...), w.Fields, w.Mask)
	if len(w.Transforms) > 0 {
		if after, results, err = applyTransforms(after, w.Transforms, now); err != nil {
			return nil, 0, err
		}
	}
	if f.over == nil {
		f.over = make(map[string]overField, len(names))
	}
	changed := after.Fields()
	f.n += len(changed) - len(before)
	if whole {
		f.odd = oddFields(changed)
	}
	for _, n := range names {
		f.over[n] = overField{}
	}
	for _, c := range changed {
		s := fieldSize(c.Name, c.Value)
		f.over[c.Name] = overField{v: c.Value, ok: true, size: s}
		size += s
		rebuilt += weight(c.Value) + s/16
	}
	f.size = size
	if f.valid {
		err = value.ValidateChanged(changed, f.n)
	} else {
		err = value.Validate(f.value())
	}
	if err != nil {
		return nil, 0, invalidDocument(err)
	}
	f.valid = true
	if size > value.MaxDocumentSize {
		return nil, 0, documentTooLarge(db.DocumentName(w.Path), size)
	}
	return results, rebuilt, nil
}

// weight is the values v holds, itself included: the work of rebuilding, checking and sizing it,
// with one more per 16 bytes of size (counted by the caller).
func weight(v value.Value) int {
	n := 1
	for _, e := range v.Elements() {
		n += weight(e)
	}
	for _, f := range v.Fields() {
		n += weight(f.Value)
	}
	return n
}

// check evaluates a precondition. update_time is matched against the stored document's: a
// document an earlier write of the same commit created has none yet.
func (d *docChange) check(db DB, c Precondition) *Error {
	if c.Exists != nil {
		switch {
		case *c.Exists && !d.exists:
			return noDocumentToUpdate(db.DocumentName(d.path))
		case !*c.Exists && d.exists:
			return alreadyExists(db.DocumentName(d.path))
		}
	}
	if !c.UpdateTime.IsZero() {
		var stored time.Time
		if d.exists && d.before != nil {
			stored = d.before.updateTime
		}
		if !stored.Equal(c.UpdateTime) {
			return staleVersion(stored, c.UpdateTime)
		}
	}
	return nil
}

// results returns each write's result once the commit took commitTime. Every write of a
// document has the document's update_time as the commit leaves it, none when it leaves it absent:
// an update followed by a delete of it has none, a delete followed by an update has it
// (f2-commit.json same.update_delete, same.delete_update).
func (p *plan) results(commitTime time.Time) []WriteResult {
	out := make([]WriteResult, len(p.writes))
	for i := range p.writes {
		out[i].TransformResults = p.transformed[i]
		d := p.of[i]
		if !d.exists {
			continue
		}
		switch {
		case d.changed():
			out[i].UpdateTime = commitTime
		case d.before != nil:
			out[i].UpdateTime = d.before.updateTime
		}
	}
	return out
}

// Change is one document's change in a commit, as the hub hears of it: Before and After are the
// document as it was and as the commit left it (Exists false for absent).
type Change struct {
	Path   Path
	Before Document
	After  Document
}

// changes returns the changes the commit makes, in lock order.
func (p *plan) changes(commitTime time.Time) []Change {
	var out []Change
	for _, d := range p.docs {
		if !d.changed() {
			continue
		}
		c := Change{Path: d.path, Before: Document{Path: d.path}, After: Document{Path: d.path}}
		if d.before != nil {
			c.Before.Fields, c.Before.CreateTime, c.Before.UpdateTime = d.before.fields, d.before.createTime, d.before.updateTime
		}
		if d.exists {
			c.After.Fields, c.After.CreateTime, c.After.UpdateTime = d.fields, commitTime, commitTime
			if d.before != nil {
				c.After.CreateTime = d.before.createTime
			}
		}
		out = append(out, c)
	}
	return out
}

// encodedDoc is a changed document as the stamp statement writes it (stampArgs).
type encodedDoc struct {
	doc, before, after, mask []byte
	size                     int32
	err                      error
}

// stampArgs fills the stamp statement's arrays from the changed documents, encoded in parallel
// (indexArgs), each with its expire_time under cat's TTL policies; the first document in order
// that fails gives the error.
func (p *plan) stampArgs(a *stampInput, cat *catalog) error {
	var changed []*docChange
	for _, d := range p.docs {
		if d.changed() {
			changed = append(changed, d)
		}
	}
	enc := make([]encodedDoc, len(changed))
	parallel(len(changed), func(i int) { enc[i] = changed[i].encode() })
	for i, d := range changed {
		e := enc[i]
		if e.err != nil {
			return e.err
		}
		switch {
		case d.before != nil && d.exists:
			a.updateKeys = append(a.updateKeys, d.keys.stored)
			a.updateSeqs = append(a.updateSeqs, d.before.seq)
			a.updateDocs = append(a.updateDocs, e.doc)
			a.updateSizes = append(a.updateSizes, e.size)
			a.updateExpires = append(a.updateExpires, cat.storedExpiry(d.path, d.fields))
			a.replaced = append(a.replaced, d.keys.stored)
		case d.exists:
			a.insertKeys = append(a.insertKeys, d.keys.stored)
			a.insertNames = append(a.insertNames, d.keys.name)
			a.insertParentHashes = append(a.insertParentHashes, d.keys.parentHash)
			a.insertCgHashes = append(a.insertCgHashes, d.keys.cgHash)
			a.insertParents = append(a.insertParents, d.keys.parent)
			a.insertCollectionIDs = append(a.insertCollectionIDs, d.path.CollectionID())
			a.insertDocs = append(a.insertDocs, e.doc)
			a.insertSizes = append(a.insertSizes, e.size)
			a.insertExpires = append(a.insertExpires, cat.storedExpiry(d.path, d.fields))
		default:
			a.deleteKeys = append(a.deleteKeys, d.keys.stored)
			a.deleteSeqs = append(a.deleteSeqs, d.before.seq)
			a.replaced = append(a.replaced, d.keys.stored)
		}
		a.logKeys = append(a.logKeys, d.keys.stored)
		a.logNames = append(a.logNames, d.keys.name)
		a.logBefores = append(a.logBefores, e.before)
		a.logAfters = append(a.logAfters, e.after)
		a.logMasks = append(a.logMasks, e.mask)
	}
	return nil
}

// encode encodes the document as the commit writes it, its changelog images and its size.
func (d *docChange) encode() encodedDoc {
	var e encodedDoc
	if d.exists {
		if e.doc, e.err = encodeDocument(d.fields, time.Time{}, time.Time{}); e.err != nil {
			return e
		}
	}
	e.before, e.after, e.mask, e.err = d.logImages(e.doc)
	e.size = int32(value.DocumentSize(d.path.String(), d.fields))
	return e
}

// logImages encodes the changelog's images (migration documents/00001, changelog): before with
// its times; after with its create_time when it existed before, its update_time being the
// commit's; nil for an absent document. The mask is the one masked write's, if it was alone.
// doc is the document's fields encoded, as written: deterministic encoding writes a Document's
// fields before its times, so an image is the fields' encoding with its times' appended, and
// the document before is not encoded again from its fields when its stored form was read.
func (d *docChange) logImages(doc []byte) (before, after, mask []byte, err error) {
	if d.before != nil {
		if d.before.stored != nil {
			before, err = appendTimes(d.before.stored, d.before.createTime, d.before.updateTime)
		} else {
			before, err = encodeDocument(d.before.fields, d.before.createTime, d.before.updateTime)
		}
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if d.exists {
		var created time.Time
		if d.before != nil {
			created = d.before.createTime
		}
		if after, err = appendTimes(doc, created, time.Time{}); err != nil {
			return nil, nil, nil, err
		}
	}
	if d.writes == 1 && d.masked {
		m := &firestorepb.DocumentMask{FieldPaths: make([]string, len(d.mask))}
		for i, f := range d.mask {
			m.FieldPaths[i] = f.String()
		}
		if mask, err = proto.Marshal(m); err != nil {
			return nil, nil, nil, err
		}
	}
	return before, after, mask, nil
}
